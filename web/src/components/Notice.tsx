export type Notice = { kind: 'ok' | 'error'; text: string } | null

export function NoticeBanner({ notice }: { notice: Notice }) {
  if (!notice) {
    return null
  }
  return (
    <p
      className={notice.kind === 'error' ? 'notice error' : 'notice ok'}
      role={notice.kind === 'error' ? 'alert' : 'status'}
      aria-live="polite"
    >
      {notice.text}
    </p>
  )
}
