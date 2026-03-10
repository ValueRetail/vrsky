import ConsumerNode from '../components/Pipeline/ConsumerNode'
import FilterNode from '../components/Pipeline/FilterNode'
import ConverterNode from '../components/Pipeline/ConverterNode'
import ProducerNode from '../components/Pipeline/ProducerNode'

// NodeTypes defined at module level, completely outside component scope
// Frozen to prevent React Flow from detecting object reference changes during HMR/StrictMode
export const nodeTypes = Object.freeze({
  consumer: ConsumerNode,
  filter: FilterNode,
  converter: ConverterNode,
  producer: ProducerNode,
})
