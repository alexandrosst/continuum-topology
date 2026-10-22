
import { processorProblems, type ProcessorEntry } from '../../../lib/processorCatalog'

/**
 * What is wrong with the extra-processor draft for a regional operator, in words a person can act on -
 * empty when it is fine. Just a thin, named re-export of processorCatalog's own check: this file's job is
 * pairing it with the operator page's own use of it, not reimplementing it.
 */
export const operatorProcessorProblems = (entries: ProcessorEntry[]): string[] => processorProblems(entries)
