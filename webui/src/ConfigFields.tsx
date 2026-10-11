import { RcloneOptions } from './RcloneOptions'
import { ServerPathPicker } from './ServerPathPicker'
import { Button, Form, Input, InputNumber, Select, Space, Switch } from 'antd'
import type { CredentialField, SettingField } from './configSchema'

function FieldLabel({ label, required, bold }: { label: string; required?: boolean; bold?: boolean }) {
  return (bold ?? required) ? <strong>{label}</strong> : <>{label}</>
}

function settingInput(field: SettingField) {
  switch (field.input) {
    case 'rcloneOptions':
      return <RcloneOptions />
    case 'serverPath':
      return <ServerPathPicker kind={field.selection} label={field.label} placeholder={field.placeholder} />
    case 'switch':
      return <Switch />
    case 'number':
      return <InputNumber min={0} precision={field.precision} style={{ width: '100%' }} />
    case 'select':
      return <Select allowClear={!field.required} placeholder={field.placeholder} options={field.options} />
    case 'text':
      return <Input placeholder={field.placeholder} />
  }
}

export function SettingControl({ field }: { field: SettingField }) {
  const help = [field.help, field.defaultValue !== undefined && `默认值：${String(field.defaultValue)}`].filter(Boolean).join('；')
  return <Form.Item
    name={field.path}
    valuePropName={field.input === 'switch' ? 'checked' : 'value'}
    label={<FieldLabel {...field} />}
    extra={help}
    rules={field.required ? [{ required: true, whitespace: true, message: `请填写${field.label}` }] : []}
  >
    {settingInput(field)}
  </Form.Item>
}

export function CredentialControl({ field, value, configured, onChange }: {
  field: CredentialField
  value?: string
  configured: boolean
  onChange: (value?: string) => void
}) {
  let help = field.required ? `必填，请输入${field.label}` : '未配置；可留空'
  if (configured) help = field.loadValue ? '当前数据库中的 TOKEN，可直接修改；保存后立即验证或刷新' : '已配置；留空保留原值'
  if (value === '') help = '保存后将清除该凭据'

  return <Form.Item label={<FieldLabel {...field} />} required={field.required} extra={help}>
    <Space.Compact style={{ width: '100%' }}>
      <Input.Password
        aria-label={field.label}
        autoComplete="new-password"
        value={value || ''}
        placeholder={configured ? '已配置（隐藏）' : '请输入'}
        onChange={event => onChange(field.loadValue ? event.target.value : (event.target.value || undefined))}
      />
      <Button danger disabled={field.required || (!configured && !value)} onClick={() => onChange('')}>清除</Button>
    </Space.Compact>
  </Form.Item>
}
