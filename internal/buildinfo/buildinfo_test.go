package buildinfo

import "testing"

func TestBaseReleaseVersion(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "0.8.0", want: "0.8.0"},
		{input: "v0.8.0", want: "0.8.0"},
		{input: "0.8.0fix1", want: "0.8.0"},
		{input: "v0.8.0fix1", want: "0.8.0"},
		{input: " 0.8.0fix1 ", want: "0.8.0"},
		{input: "0.5.1fix", want: "0.5.1"},
		{input: "0.5.1fix18", want: "0.5.1"},
		{input: "dev", want: "dev"},
		{input: "0.8.0fix0", want: "0.8.0fix0"},
		{input: "0.8.0fix01", want: "0.8.0fix01"},
		{input: "0.8.0-rc.1", want: "0.8.0-rc.1"},
		{input: "0.8fix1", want: "0.8fix1"},
		{input: "0.8.0fix1-rc.1", want: "0.8.0fix1-rc.1"},
		{input: "0.8.0fix1+meta", want: "0.8.0fix1+meta"},
		{input: "01.8.0fix1", want: "01.8.0fix1"},
	}

	for _, tc := range tests {
		if got := BaseReleaseVersion(tc.input); got != tc.want {
			t.Errorf("BaseReleaseVersion(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
