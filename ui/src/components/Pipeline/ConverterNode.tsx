import { Handle, Position } from 'reactflow'

interface NodeData {
  label: string
  config?: Record<string, unknown>
}

export default function ConverterNode({ data }: { data: NodeData }) {
  return (
    <div className="px-4 py-3 shadow-lg rounded-lg bg-gradient-to-b from-purple-500 to-purple-600 text-white border-2 border-purple-700 cursor-pointer hover:shadow-xl hover:from-purple-600 hover:to-purple-700 transition-all">
      <div className="font-bold text-sm">🔄 {data.label}</div>
      <div className="text-xs opacity-75 mt-1">Transform</div>
      <Handle type="target" position={Position.Left} />
      <Handle type="source" position={Position.Right} />
    </div>
  )
}
