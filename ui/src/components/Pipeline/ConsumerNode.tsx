import { Handle, Position } from 'reactflow'

interface NodeData {
  label: string
  config?: Record<string, unknown>
}

export default function ConsumerNode({ data }: { data: NodeData & { label: string } }) {
  return (
    <div className="px-4 py-3 shadow-lg rounded-lg bg-gradient-to-b from-blue-500 to-blue-600 text-white border-2 border-blue-700 cursor-pointer hover:shadow-xl hover:from-blue-600 hover:to-blue-700 transition-all">
      <div className="font-bold text-sm">📥 {data.label}</div>
      <div className="text-xs opacity-75 mt-1">Source</div>
      <Handle type="source" position={Position.Right} />
    </div>
  )
}
