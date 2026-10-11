import { Button, Input, Space } from 'antd'
import { PlusOutlined, MinusCircleOutlined } from '@ant-design/icons'

export function RcloneOptions({ value, onChange }: { value?: string[] | null; onChange?: (value: string[]) => void }) {
  const rows = value || []
  return <Space orientation="vertical" style={{ width: '100%' }}>
    {rows.map((row, index) => <Space.Compact key={index} style={{ width: '100%' }}>
      <Input aria-label={`rclone 参数 ${index + 1}`} placeholder="--vfs-read-chunk-size=128M" value={row} onChange={e => onChange?.(rows.map((value, i) => i === index ? e.target.value : value))} />
      <Button aria-label={`删除 rclone 参数 ${index + 1}`} icon={<MinusCircleOutlined />} danger onClick={() => onChange?.(rows.filter((_, i) => i !== index))} />
    </Space.Compact>)}
    <Button icon={<PlusOutlined />} onClick={() => onChange?.([...rows, ''])}>添加 rclone 参数</Button>
  </Space>
}
