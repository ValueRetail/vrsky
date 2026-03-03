import { Handle, Position } from 'reactflow'

interface NodeData {
  label: string
  config?: Record<string, unknown>
}

export default function FilterNode({ data }: { data: NodeData }) {
  return (
    <div className="px-4 py-3 shadow-lg rounded-lg bg-gradient-to-b from-yellow-500 to-yellow-600 text-white border-2 border-yellow-700 cursor-pointer hover:shadow-xl hover:from-yellow-600 hover:to-yellow-700 transition-all">
      <div className="font-bold text-sm">🔍 {data.label}</div>
      <div className="text-xs opacity-75 mt-1">Conditions</div>
      <Handle type="target" position={Position.Left} />
      <Handle type="source" position={Position.Right} />
    </div>
  )
}
