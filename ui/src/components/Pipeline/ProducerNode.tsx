import { Handle, Position } from 'reactflow'

interface NodeData {
  label: string
  config?: Record<string, unknown>
}

export default function ProducerNode({ data }: { data: NodeData }) {
  return (
    <div className="px-4 py-3 shadow-lg rounded-lg bg-gradient-to-b from-green-500 to-green-600 text-white border-2 border-green-700 cursor-pointer hover:shadow-xl hover:from-green-600 hover:to-green-700 transition-all">
      <div className="font-bold text-sm">📤 {data.label}</div>
      <div className="text-xs opacity-75 mt-1">Destination</div>
      <Handle type="target" position={Position.Left} />
    </div>
  )
}
