package kernel

import (
	"strings"
	"testing"
)

// TestSchemaPromptProfitFormulaExcludesLeverage pins down a real bug found by
// tracing the AI's own reasoning in production: the Profit (Realized PnL,
// USDT) formula multiplied by Leverage, which double-counts it. Position
// Value is already the full notional exposure (Margin = Position Value /
// Leverage elsewhere in this same dictionary), so dollar PnL for a given
// price move doesn't scale with leverage - only PnL% (return on margin)
// does. The AI was observed spending many reasoning steps trying to
// reconcile a $50 vs $5 risk estimate for the same trade because it had been
// taught this formula as a template for dollar amounts.
func TestSchemaPromptProfitFormulaExcludesLeverage(t *testing.T) {
	prompt := GetSchemaPrompt(LangEnglish)

	if !strings.Contains(prompt, "(Exit Price - Entry Price) / Entry Price × Position Value") {
		t.Fatalf("schema prompt missing the corrected (no-leverage) Profit formula:\n%s", prompt)
	}
	if strings.Contains(prompt, "(Exit Price - Entry Price) / Entry Price × Leverage × Position Value") {
		t.Fatalf("schema prompt still contains the buggy leveraged Profit formula:\n%s", prompt)
	}
	// PnL% is a return-on-margin percentage, where the × Leverage term is
	// correct and must stay - only the USDT Profit formula was wrong.
	if !strings.Contains(prompt, "(Exit - Entry) / Entry × Leverage × 100") {
		t.Fatalf("schema prompt should still leverage-scale PnL%% (return on margin):\n%s", prompt)
	}
}
