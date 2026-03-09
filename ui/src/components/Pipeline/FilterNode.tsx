import { Handle, Position } from 'reactflow'

interface NodeData {
  label: string
  config?: Record<string, unknown>
}

export default function FilterNode({ data }: { data: NodeData }) {
  return (
    <div className="px-4 py-2 shadow-md rounded-lg bg-yellow-400 text-white border-2 border-yellow-500 cursor-pointer hover:shadow-lg hover:bg-yellow-500 transition-all font-medium text-sm">
      {data.label}
      <Handle type="target" position={Position.Left} />
      <Handle type="source" position={Position.Right} />
    </div>
  )
}
