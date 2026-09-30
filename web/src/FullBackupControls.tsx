import { useRef, useState, type FormEvent } from 'react'

export type BackupInput = { mode: 'metadata' | 'full'; encrypt: boolean; passphrase?: string }
export function FullBackupControls({ create }: { create: (input: BackupInput) => Promise<void> }) {
  const [mode, setMode] = useState<'metadata' | 'full'>('metadata')
  const [encryptMetadata, setEncryptMetadata] = useState(false)
  const [passphrase, setPassphrase] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const submitting=useRef(false)
  const encrypted = mode === 'full' || encryptMetadata
  async function submit(event: FormEvent) {
    event.preventDefault()
    if(submitting.current)return
    if (encrypted && (!passphrase || passphrase !== confirmation)) { setError('请输入口令，并确认两次输入一致。'); return }
    const input: BackupInput = { mode, encrypt: encrypted, ...(encrypted ? { passphrase } : {}) }
    submitting.current=true;setBusy(true); setError(''); setPassphrase(''); setConfirmation('')
    try { await create(input) } catch (e) { setError(e instanceof Error ? e.message : '备份未完成，请查看操作状态。') } finally { submitting.current=false;setBusy(false) }
  }
  return <form onSubmit={submit}>
    <label>备份内容<select value={mode} onChange={e => setMode(e.target.value as 'metadata' | 'full')} disabled={busy}>
      <option value="metadata">元数据与历史报表</option><option value="full">完整数据与本机凭据</option>
    </select></label>
    {mode === 'metadata' && <label><input type="checkbox" checked={encryptMetadata} onChange={e => setEncryptMetadata(e.target.checked)} disabled={busy} />使用口令加密</label>}
    {mode === 'full' && <p>完整备份包含凭据，必须加密。服务会暂停新请求并等待正在写入的操作结束；未排空时备份失败并恢复服务。</p>}
    {encrypted && <><label>备份口令<input type="password" autoComplete="new-password" value={passphrase} onChange={e => setPassphrase(e.target.value)} disabled={busy} /></label>
      <label>确认口令<input type="password" autoComplete="new-password" value={confirmation} onChange={e => setConfirmation(e.target.value)} disabled={busy} /></label>
      <p>口令只用于本次备份。请自行保存；丢失后无法解密。</p></>}
    <button type="submit" disabled={busy}>{busy ? '正在提交…' : '创建备份'}</button>
    {error && <p role="alert">{error}</p>}
  </form>
}
export function EncryptedRestoreControls({ preview, disabled=false }: { disabled?:boolean; preview: (file: File, target: string, passphrase: string) => Promise<void> }) {
  const [file, setFile] = useState<File | null>(null)
  const [target, setTarget] = useState('')
  const [passphrase, setPassphrase] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const submitting=useRef(false)
  async function submit(event: FormEvent) { event.preventDefault();if(disabled||submitting.current)return; if (!file || !target) { setError('请选择备份文件与全新目标目录。'); return }
    submitting.current=true;setBusy(true); setError(''); const phrase = passphrase; setPassphrase('')
    try { await preview(file, target, phrase) } catch (e) { setError(e instanceof Error ? e.message : '恢复预览失败，原目录保留。') } finally { submitting.current=false;setBusy(false) }
  }
  return <form onSubmit={submit}>
    <label>备份文件<input type="file" accept=".tar,.age" onChange={e => setFile(e.target.files?.[0] ?? null)} disabled={busy||disabled} /></label>
    <label>全新目标目录<input value={target} onChange={e => setTarget(e.target.value)} disabled={busy||disabled} /></label>
    <label>加密文件口令<input type="password" autoComplete="off" value={passphrase} onChange={e => setPassphrase(e.target.value)} disabled={busy||disabled} /></label>
    <button type="submit" disabled={busy||disabled}>{busy ? '正在校验…' : '预览恢复'}</button>
    {error && <p role="alert">{error}</p>}
  </form>
}
