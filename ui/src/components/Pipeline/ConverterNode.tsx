import { Handle, Position } from 'reactflow'

interface NodeData {
  label: string
  config?: Record<string, unknown>
}

export default function ConverterNode({ data }: { data: NodeData }) {
  return (
    <div className="px-4 py-2 shadow-md rounded-lg bg-pink-500 text-white border-2 border-pink-600 cursor-pointer hover:shadow-lg hover:bg-pink-600 transition-all font-medium text-sm">
      {data.label}
      <Handle type="target" position={Position.Left} />
      <Handle type="source" position={Position.Right} />
    </div>
  )
}
