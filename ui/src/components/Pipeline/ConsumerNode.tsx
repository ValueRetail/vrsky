import { Handle, Position } from 'reactflow'

interface NodeData {
  label: string
  config?: Record<string, unknown>
}

export default function ConsumerNode({ data }: { data: NodeData & { label: string } }) {
  return (
    <div className="px-4 py-2 shadow-md rounded-lg bg-blue-400 text-white border-2 border-blue-500 cursor-pointer hover:shadow-lg hover:bg-blue-500 transition-all font-medium text-sm">
      {data.label}
      <Handle type="source" position={Position.Right} />
    </div>
  )
}
