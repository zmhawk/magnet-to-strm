import { useState, type Key } from 'react'
import { Alert, Button, Input, Modal, Space, Spin, Tree, Typography } from 'antd'
import type { DataNode } from 'antd/es/tree'
import { DatabaseOutlined, FolderOutlined } from '@ant-design/icons'
import { api, type ServerPathNode } from './api'

interface PathTreeNode extends DataNode {
  path: string
  is_directory: boolean
  children?: PathTreeNode[]
}

function treeNodes(nodes: ServerPathNode[], parentKey = ''): PathTreeNode[] {
  return nodes.map(node => ({
    ...node,
    key: parentKey ? `${parentKey}/${encodeURIComponent(node.path)}` : node.key,
    icon: node.is_directory ? <FolderOutlined /> : <DatabaseOutlined />,
  }))
}

function attachChildren(nodes: PathTreeNode[], key: Key, children: PathTreeNode[]): PathTreeNode[] {
  return nodes.map(node => node.key === key
    ? { ...node, children, isLeaf: children.length === 0 }
    : { ...node, children: node.children && attachChildren(node.children, key, children) })
}

export function ServerPathPicker({ kind, label, value, onChange, id, placeholder }: {
  kind: 'database' | 'directory'
  label: string
  value?: string
  onChange?: (value: string) => void
  id?: string
  placeholder?: string
}) {
  const [open, setOpen] = useState(false)
  const [loading, setLoading] = useState(false)
  const [nodes, setNodes] = useState<PathTreeNode[]>([])
  const [selected, setSelected] = useState<PathTreeNode | null>(null)
  const [error, setError] = useState('')

  async function browse() {
    setOpen(true); setLoading(true); setSelected(null); setNodes([]); setError('')
    try { setNodes(treeNodes((await api.paths(kind)).nodes)) }
    catch (error) { setError(error instanceof Error ? error.message : '读取目录失败') }
    finally { setLoading(false) }
  }

  async function loadChildren(node: PathTreeNode) {
    setError('')
    try {
      const result = await api.paths(kind, node.path)
      setNodes(previous => attachChildren(previous, node.key, treeNodes(result.nodes, String(node.key))))
    } catch (error) {
      setError(error instanceof Error ? error.message : '读取目录失败')
    }
  }

  const selection = selected && (kind === 'database' && selected.is_directory
    ? `${selected.path.replace(/\/$/, '')}/magnet.db`
    : selected.path)

  return <>
    <Space.Compact style={{ width: '100%' }}>
      <Input id={id} aria-label={label} value={value || ''} onChange={event => onChange?.(event.target.value)} placeholder={placeholder || "输入或选择服务端路径"} />
      <Button icon={<FolderOutlined />} onClick={() => void browse()} aria-label={`选择${label}`}>选择</Button>
    </Space.Compact>
    <Modal
      title={`选择${label}`} open={open} onCancel={() => setOpen(false)}
      okText="使用此路径" cancelText="取消" okButtonProps={{ disabled: !selection }}
      onOk={() => { if (selection) onChange?.(selection); setOpen(false) }} destroyOnHidden
    >
      <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
        <Typography.Text type="secondary">
          {kind === 'database' ? '可选择目录或 .db、.sqlite、.sqlite3 文件。' : '请选择输出目录。'}
        </Typography.Text>
        {error && <Alert type="error" title={error} action={<Button size="small" onClick={() => void browse()}>重新加载</Button>} />}
        {loading ? <Spin /> : <Tree<PathTreeNode>
          showIcon blockNode treeData={nodes} loadData={loadChildren}
          selectedKeys={selected ? [selected.key] : []}
          onSelect={(_keys, info) => setSelected(info.selected ? info.node : null)}
          style={{ maxHeight: 400, overflow: 'auto' }}
        />}
        <Typography.Text style={{ overflowWrap: 'anywhere' }}>保存路径：{selection || '尚未选择'}</Typography.Text>
      </Space>
    </Modal>
  </>
}
