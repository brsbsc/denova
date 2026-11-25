package external

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHelperExternalProcessTreeChild(t *testing.T) {
	role := os.Getenv("DENOVA_TEST_PROCESS_TREE_ROLE")
	switch role {
	case "":
		return
	case "grandchild":
		fmt.Println(os.Getpid())
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "child":
		exe, err := os.Executable()
		if err != nil {
			os.Exit(2)
		}
		grandchild := exec.Command(exe, "-test.run=^TestHelperExternalProcessTreeChild$")
		grandchild.Env = append(os.Environ(), "DENOVA_TEST_PROCESS_TREE_ROLE=grandchild")
		stdout, err := grandchild.StdoutPipe()
		if err != nil {
			os.Exit(2)
		}
		if err := grandchild.Start(); err != nil {
			os.Exit(2)
		}
		scanner := bufio.NewScanner(stdout)
		if !scanner.Scan() {
			os.Exit(2)
		}
		fmt.Println(scanner.Text())
		_ = grandchild.Wait()
		os.Exit(0)
	}
}

func TestOwnProcessTreeTerminatesSpawnedChildAndGrandchild(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestHelperExternalProcessTreeChild$")
	cmd.Env = append(os.Environ(), "DENOVA_TEST_PROCESS_TREE_ROLE=child")
	ConfigureProcess(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	killTree := OwnProcessTree(cmd.Process)

	pidCh := make(chan int, 1)
	errCh := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				errCh <- fmt.Errorf("reader panic: %v", r)
			}
		}()
		scanner := bufio.NewScanner(stdout)
		if !scanner.Scan() {
			errCh <- fmt.Errorf("failed to read grandchild pid: %v", scanner.Err())
			return
		}
		pid, parseErr := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if parseErr != nil {
			errCh <- parseErr
			return
		}
		pidCh <- pid
	}()

	var grandchildPID int
	select {
	case grandchildPID = <-pidCh:
	case readErr := <-errCh:
		killTree()
		_ = cmd.Wait()
		t.Fatal(readErr)
	case <-time.After(2 * time.Second):
		killTree()
		_ = cmd.Wait()
		t.Fatal("timed out waiting for grandchild pid")
	}

	killTree()
	waitDone := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				waitDone <- fmt.Errorf("wait panic: %v", r)
			}
		}()
		waitDone <- cmd.Wait()
	}()
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("child process did not terminate after OwnProcessTree cleanup")
	}

	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !testProcessExists(grandchildPID) {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("grandchild process %d still alive after OwnProcessTree cleanup", grandchildPID)
}
