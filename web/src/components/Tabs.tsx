import { useRef, type KeyboardEvent } from 'react'

export interface TabDefinition<T extends string> {
  value: T
  label: string
}

interface TabsProps<T extends string> {
  tabs: Array<TabDefinition<T>>
  active: T
  onChange: (value: T) => void
}

// Tabs implements the WAI-ARIA tabs pattern: a single focusable selected tab
// (roving tabindex), Arrow/Home/End navigation, and focus follows selection.
export function Tabs<T extends string>({ tabs, active, onChange }: TabsProps<T>) {
  const refs = useRef<Record<string, HTMLButtonElement | null>>({})

  const move = (event: KeyboardEvent<HTMLDivElement>) => {
    const index = tabs.findIndex((tab) => tab.value === active)
    if (index === -1) {
      return
    }
    let next = -1
    switch (event.key) {
      case 'ArrowRight':
        next = (index + 1) % tabs.length
        break
      case 'ArrowLeft':
        next = (index - 1 + tabs.length) % tabs.length
        break
      case 'Home':
        next = 0
        break
      case 'End':
        next = tabs.length - 1
        break
      default:
        return
    }
    event.preventDefault()
    const value = tabs[next].value
    onChange(value)
    refs.current[value]?.focus()
  }

  return (
    <div className="tabs" role="tablist" aria-label="Sections" onKeyDown={move}>
      {tabs.map((tab) => (
        <button
          key={tab.value}
          ref={(element) => {
            refs.current[tab.value] = element
          }}
          type="button"
          role="tab"
          id={`tab-${tab.value}`}
          aria-selected={active === tab.value}
          aria-controls={`panel-${tab.value}`}
          tabIndex={active === tab.value ? 0 : -1}
          className={active === tab.value ? 'tab active' : 'tab'}
          onClick={() => onChange(tab.value)}
        >
          {tab.label}
        </button>
      ))}
    </div>
  )
}
