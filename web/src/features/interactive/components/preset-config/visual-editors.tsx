import { useEffect, useState, type ReactNode } from 'react'
import { Copy, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'
import type { EventPackageModule, TellerEventCard } from '../../types'
import { presetDetailScrollPaneClassName as detailScrollPaneClassName, presetFieldGridClassName as fieldGridClassName, presetIconActionClassName as iconActionClassName, presetInputClassName as inputClassName } from './editor-styles'
import { PresetField as Field } from './PresetField'
import { PresetTabsList } from './PresetTabsList'
import { cloneWithNewId, itemKey, joinListInput, nextPresetId, splitListInput } from './utils'

const visualEditorShellClassName = 'preset-visual-editor-shell grid h-full min-h-0 min-w-0 gap-3 overflow-hidden'

export function EventPackageVisualEditor({
  value,
  onChange,
  onValidityChange,
}: {
  value: EventPackageModule
  onChange: (value: EventPackageModule) => void
  onValidityChange: (valid: boolean) => void
}) {
  const { t } = useTranslation()
  const [activeCardId, setActiveCardId] = useState('')
  const cards = value.events || []

  useEffect(() => onValidityChange(true), [onValidityChange])
  useEffect(() => {
    if (!cards.some((card, index) => itemKey(card, index, 'card') === activeCardId)) {
      setActiveCardId(cards[0] ? itemKey(cards[0], 0, 'card') : '')
    }
  }, [activeCardId, cards])

  const setCards = (events: TellerEventCard[]) => onChange({ ...value, events })
  const activeIndex = cards.findIndex((card, index) => itemKey(card, index, 'card') === activeCardId)
  const activeCard = activeIndex >= 0 ? cards[activeIndex] : null
  const patchCard = (patch: Partial<TellerEventCard>) => {
    if (!activeCard) return
    const nextCard = { ...activeCard, ...patch }
    if (patch.id !== undefined) setActiveCardId(itemKey(nextCard, activeIndex, 'card'))
    setCards(cards.map((card, index) => (index === activeIndex ? nextCard : card)))
  }
  const addCard = () => {
    const card: TellerEventCard = { id: nextPresetId('event-card'), type_name: '', enabled: true }
    setCards([...cards, card])
    setActiveCardId(card.id || '')
  }
  const copyCard = () => {
    if (!activeCard) return
    const card = cloneWithNewId(activeCard, 'event-card')
    setCards([...cards, card])
    setActiveCardId(card.id || '')
  }
  const deleteCard = () => {
    if (!activeCard) return
    const next = cards.filter((_, index) => index !== activeIndex)
    setCards(next)
    setActiveCardId(next[0] ? itemKey(next[0], 0, 'card') : '')
  }

  return (
    <div className={visualEditorShellClassName} data-testid="event-package-card-editor">
      <PresetTabsList
        items={cards}
        activeId={activeCardId}
        getId={(card, index) => itemKey(card, index, 'card')}
        getTitle={(card, index) => card.type_name || card.id || `${t('settingPanel.presetConfig.eventCard')} ${index + 1}`}
        getSubtitle={(card) => [card.category, card.intensity].filter(Boolean).join(' · ')}
        addLabel={t('settingPanel.presetConfig.addEventCard')}
        emptyLabel={t('settingPanel.presetConfig.eventCards')}
        layout="rail"
        testIdPrefix="event-package-cards"
        onAdd={addCard}
        onActiveIdChange={setActiveCardId}
        onItemsChange={setCards}
      />
      <div className={detailScrollPaneClassName} data-testid="event-package-card-detail-scroll">
        {activeCard ? <EventCardDetails item={activeCard} onPatch={patchCard} onCopy={copyCard} onDelete={deleteCard} /> : <EmptyDetail>{t('settingPanel.presetConfig.emptyEventCards')}</EmptyDetail>}
      </div>
    </div>
  )
}

function EventCardDetails({
  item,
  onPatch,
  onCopy,
  onDelete,
}: {
  item: TellerEventCard
  onPatch: (patch: Partial<TellerEventCard>) => void
  onCopy: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation()
  return (
    <DetailPanel dense>
      <DetailActions onCopy={onCopy} onDelete={onDelete} />
      <div className={fieldGridClassName}>
        <Field label={t('settingPanel.presetConfig.id')}><Input className={inputClassName} value={item.id || ''} onChange={(event) => onPatch({ id: event.target.value })} /></Field>
        <Field label={t('settingPanel.presetConfig.typeName')}><Input className={inputClassName} value={item.type_name || ''} onChange={(event) => onPatch({ type_name: event.target.value })} /></Field>
        <Field label={t('settingPanel.orchestration.category')}><Input className={inputClassName} value={item.category || ''} onChange={(event) => onPatch({ category: event.target.value })} /></Field>
        <Field label={t('settingPanel.field.tags')}><Input className={inputClassName} value={joinListInput(item.tags)} onChange={(event) => onPatch({ tags: splitListInput(event.target.value) })} /></Field>
        <Field label={t('settingPanel.presetConfig.intensity')}><Input className={inputClassName} value={item.intensity || ''} onChange={(event) => onPatch({ intensity: event.target.value })} /></Field>
        <SwitchField label={t('settingPanel.field.enabled')} checked={item.enabled !== false} onChange={(enabled) => onPatch({ enabled })} />
      </div>
      <Field label={t('settingPanel.presetConfig.descriptionMarkdown')}>
        <Textarea autoResize={false} className="nova-field min-h-28 resize-y text-xs leading-5 shadow-none focus-visible:ring-0" value={item.description_markdown || ''} onChange={(event) => onPatch({ description_markdown: event.target.value })} />
      </Field>
    </DetailPanel>
  )
}

function DetailPanel({
  children,
  dense = false,
  className = '',
  title,
  description,
  meta,
  actions,
}: {
  children: ReactNode
  dense?: boolean
  className?: string
  title?: string
  description?: string
  meta?: string
  actions?: ReactNode
}) {
  return (
    <section className={cn('grid min-w-0 gap-3 rounded-[14px] bg-[var(--nova-surface-2)]', dense ? 'p-3' : 'p-4', className)}>
      {title || description || meta || actions ? (
        <EditorSectionHeader title={title} description={description} meta={meta} actions={actions} />
      ) : null}
      {children}
    </section>
  )
}

function EmptyDetail({ children }: { children: ReactNode }) {
  return <div className="flex min-h-48 items-center justify-center rounded-[12px] border border-dashed border-[var(--nova-border)] bg-[var(--nova-surface-2)] px-3 py-8 text-center text-xs text-[var(--nova-text-faint)]">{children}</div>
}

export function EditorSectionHeader({
  title,
  description,
  meta,
  actions,
}: {
  title?: string
  description?: string
  meta?: string
  actions?: ReactNode
}) {
  return (
    <div className="flex min-w-0 flex-wrap items-start justify-between gap-3">
      <div className="min-w-0">
        {title ? <div className="truncate text-sm font-semibold text-[var(--nova-text)]">{title}</div> : null}
        {description ? <div className="mt-1 line-clamp-2 text-[11px] leading-5 text-[var(--nova-text-faint)]">{description}</div> : null}
      </div>
      {meta || actions ? (
        <div className="flex shrink-0 items-center gap-2">
          {meta ? (
            <Badge variant="outline" className="h-6 rounded-full border-[var(--nova-border)] bg-[var(--nova-surface-2)] px-2.5 text-[10px] font-normal text-[var(--nova-text-faint)]">
              {meta}
            </Badge>
          ) : null}
          {actions}
        </div>
      ) : null}
    </div>
  )
}

function DetailActions({ onCopy, onDelete }: { onCopy?: () => void; onDelete?: () => void }) {
  const { t } = useTranslation()
  return (
    <div className="flex justify-end gap-2">
      <Button className={iconActionClassName} variant="outline" size="icon-sm" disabled={!onCopy} onClick={onCopy} aria-label={t('settingPanel.presetConfig.copy')}>
        <Copy data-icon="inline-start" />
      </Button>
      <Button className={iconActionClassName} variant="outline" size="icon-sm" disabled={!onDelete} onClick={onDelete} aria-label={t('common.delete')}>
        <Trash2 data-icon="inline-start" />
      </Button>
    </div>
  )
}

function SwitchField({ label, checked, onChange }: { label: string; checked: boolean; onChange: (checked: boolean) => void }) {
  return (
    <div className="flex items-end">
      <div className="flex h-9 w-full items-center justify-between gap-2 rounded-[14px] border border-[var(--nova-border)] bg-[var(--nova-surface-2)] px-3 shadow-[inset_0_1px_0_rgba(255,255,255,0.04)]">
        <span className="truncate text-[11px] text-[var(--nova-text-faint)]">{label}</span>
        <Switch checked={checked} onCheckedChange={onChange} />
      </div>
    </div>
  )
}
