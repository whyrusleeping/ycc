# Project memory

> Agent-maintained operational notes. Advisory, not normative — verify before relying.
> Design truth belongs in spec.md; procedures in plans/; work items in backlog/.

## Codebase gotchas
- 2026-07-06: Usage accounting: OpenAI reports cached tokens as a SUBSET of prompt_tokens while Anthropic reports cache reads/writes disjoint from input_tokens; engine/loop.go normalizes to disjoint classes at emit time and built-in default pricing lives in internal/config/default_pricing.go (config price_* always overrides).
