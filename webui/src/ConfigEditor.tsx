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
  const [restartRequired, setRestartRequired] = useState(false)
  const [restarting, setRestarting] = useState(false)
  const [credentials, setCredentials] = useState<Record<string, string>>({})
  const [tokenValues, setTokenValues] = useState(data.credentials_values || {})
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
      if (result.credentials_values) setTokenValues(result.credentials_values)
      setSaved(true)
      setRestartRequired(result.restart_required)
      if (result.token_refresh_error) { setError(result.token_refresh_error); message.warning(result.token_refresh_error) }
      else message.success(result.token_refreshed ? '配置已保存，115 TOKEN 已刷新并立即生效' : (result.restart_required ? '配置已保存，115 TOKEN 已验证；其他配置修改需重启生效' : '115 TOKEN 已验证并立即生效，无需重启服务'))

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
          value={field.loadValue ? (credentials[field.key] ?? tokenValues[field.key] ?? '') : credentials[field.key]}
          configured={Boolean(configured[field.key])}
          onChange={value => updateCredential(field.key, value)}
        />
  }
  return <Form form={form} layout="vertical" initialValues={initialSettings(data.settings, data.setup_required)} onFinish={values => void save(values)} disabled={saving}>
    <Space orientation="vertical" style={{ width: '100%' }} size="middle">
      <Alert type="info" showIcon title={data.setup_required ? '欢迎使用，请完成初始配置' : '服务配置'} description={data.setup_required ? '保存后服务自动重启并进入任务管理。115 TOKEN 仅保存在数据库中；其他接口凭据的非空环境变量或 .env 设置优先。' : '逐项填写并保存，重启服务后生效。115 TOKEN 留空保留已有值，修改后立即验证或刷新，无需重启；其他接口凭据的非空环境变量或 .env 设置优先。'} />
      {restarting && <Alert type="info" showIcon title="配置已保存，正在等待服务自动重启…" />}
      {error && <Alert type="error" showIcon title={error} />}
      {saved && <Alert type="success" showIcon title={restartRequired ? "配置已保存，其他配置修改需重启服务。" : "115 TOKEN 已更新并立即生效，无需重启服务。"} />}
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
