import { ReactFlowProvider } from 'reactflow'
import PipelineBuilder from './PipelineBuilder'

export default function PipelineBuilderPage() {
  return (
    <ReactFlowProvider>
      <PipelineBuilder />
    </ReactFlowProvider>
  )
}
