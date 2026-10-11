import { useEffect, useState, type ReactNode } from 'react'
import { Alert, App, Button, Card, Collapse, Drawer, Form, Skeleton, Space, Typography } from 'antd'
import { SettingOutlined } from '@ant-design/icons'
import { api, type ConfigResponse, type Settings } from './api'

import { credentialFields, fieldID, groupedFields, initialSettings, type ConfigField } from './configSchema'
import { CredentialControl, SettingControl } from './ConfigFields'

function ConfigForm({ data, onSaved }: { data: ConfigResponse; onSaved?: () => void }) {
  const [form] = Form.useForm<Settings>()
  const { message } = App.useApp()
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const [restarting, setRestarting] = useState(false)
  const [credentials, setCredentials] = useState<Record<string, string>>({})
  const [configured, setConfigured] = useState(data.credentials_configured || {})
  async function save(values: Settings) {
    const missing = credentialFields.find(field => field.required && (
      field.key in credentials ? !credentials[field.key].trim() : !configured[field.key]
    ))
    if (missing) {
      setError(`请填写${missing.label}`)
      return
    }
    setSaving(true); setError(''); setSaved(false)
    try {
      const result = await api.saveConfig(values, credentials)
      if (result.restarting) {
        setRestarting(true)
        message.success('配置已保存，服务正在自动重启')
        const target = new URL(result.restart_url || window.location.origin, window.location.href)
        const addressChanged = target.origin !== window.location.origin
        for (let attempt = 0; attempt < 60; attempt++) {
          await new Promise(resolve => setTimeout(resolve, 1000))
          try {
            if (addressChanged) {
              await fetch(new URL('/api/v1/config', target), { mode: 'no-cors', cache: 'no-store', signal: AbortSignal.timeout(1500) })
              window.location.assign(target.href)
              return
            }
            const config = await api.config()
            if (!config.setup_required && !config.setup_mode) { window.location.reload(); return }
          } catch { /* Wait for the service to finish restarting. */ }
        }
        throw new Error('配置已保存，等待服务启动超时。请检查服务日志，并在修改监听地址时访问新地址。')
      }
      setConfigured(previous => ({ ...previous, ...Object.fromEntries(Object.entries(credentials).map(([key, value]) => [key, value !== ''])) }))
      setCredentials({})
      setSaved(true)
      message.success('配置已保存，请重启服务生效')

    } catch (error) { setError(error instanceof Error ? error.message : '保存配置失败') }
    finally { setSaving(false); setRestarting(false) }
  }
  function updateCredential(key: string, value?: string) {
    setCredentials(previous => {
      const updated = { ...previous }
      if (value === undefined) delete updated[key]
      else updated[key] = value
      return updated
    })
  }
  function renderField(field: ConfigField) {
    return field.source === 'settings'
      ? <SettingControl key={fieldID(field)} field={field} />
      : <CredentialControl
          key={fieldID(field)} field={field}
          value={credentials[field.key]}
          configured={Boolean(configured[field.key])}
          onChange={value => updateCredential(field.key, value)}
        />
  }
  return <Form form={form} layout="vertical" initialValues={initialSettings(data.settings, data.setup_required)} onFinish={values => void save(values)} disabled={saving}>
    <Space orientation="vertical" style={{ width: '100%' }} size="middle">
      <Alert type="info" showIcon title={data.setup_required ? '欢迎使用，请完成初始配置' : '服务配置'} description={data.setup_required ? '保存后服务自动重启并进入任务管理。TOKEN 和密码可在下方设置，环境变量或 .env 中的非空凭据优先。' : '逐项填写并保存，重启服务后生效。TOKEN 和密码留空保留已有值，环境变量或 .env 中的非空凭据优先。'} />
      {restarting && <Alert type="info" showIcon title="配置已保存，正在等待服务自动重启…" />}
      {error && <Alert type="error" showIcon title={error} />}
      {saved && <Alert type="success" showIcon title="配置已保存，请重启服务。" />}
      {groupedFields.map(group => <Card key={group.id} title={group.title}>
        {group.blocks.map((block, index) => block.advanced
          ? <Collapse key={fieldID(block.fields[0])} style={{ marginBottom: 24 }} items={[{
              key: String(index), label: '高级配置', forceRender: true,
              children: block.fields.map(renderField),
            }]} />
          : block.fields.map(renderField))}
      </Card>)}
      <Typography.Text type="secondary" style={{ overflowWrap: 'anywhere' }}>保存位置：{data.path}</Typography.Text>
      <Space><Button type="primary" htmlType="submit" loading={saving}>保存配置</Button>{saved && onSaved && <Button onClick={onSaved}>进入任务管理</Button>}</Space>
    </Space>
  </Form>
}

export function ConfigurationGate({ children }: { children: ReactNode }) {
  const [data, setData] = useState<ConfigResponse | null>(null)
  const [error, setError] = useState('')
  const [completed, setCompleted] = useState(false)
  async function load() {
    setError('')
    try { setData(await api.config()) }
    catch (error) { setError(error instanceof Error ? error.message : '读取配置失败') }
  }
  useEffect(() => { void load() }, [])
  if (data && (!data.setup_required || completed)) return children
  return <main style={{ maxWidth: 860, margin: '0 auto', padding: '32px 20px' }}>
    <Typography.Title level={2}>Magnet to STRM · 初始配置</Typography.Title>
    {error ? <Space orientation="vertical"><Alert type="error" title={error} /><Button onClick={() => void load()}>重试</Button></Space> : data ? <ConfigForm data={data} onSaved={() => setCompleted(true)} /> : <Skeleton active />}
  </main>
}

export function ConfigEditor() {
  const [open, setOpen] = useState(false)
  const [data, setData] = useState<ConfigResponse | null>(null)
  const [error, setError] = useState('')
  async function load() {
    setOpen(true); setData(null); setError('')
    try { setData(await api.config()) }
    catch (error) { setError(error instanceof Error ? error.message : '读取配置失败') }
  }
  return <>
    <Button icon={<SettingOutlined />} onClick={() => void load()}>配置</Button>
    <Drawer title="服务配置" open={open} onClose={() => setOpen(false)} size="large" destroyOnHidden>
      {error ? <Alert type="error" title={error} /> : data ? <ConfigForm data={data} /> : <Skeleton active />}
    </Drawer>
  </>
}
