package kernel

import (
	"fmt"
	"strings"
)

func formatMultiFactorDataZH(ctx *Context) string {
	var sb strings.Builder
	if w := ctx.DecisionWeights; w != nil {
		sb.WriteString(fmt.Sprintf("## 决策权重\n\n技术 %d%% | 情绪 %d%% | 估值 %d%% | 基本面 %d%%\n\n",
			w.TechnicalWeight, w.SentimentWeight, w.ValuationWeight, w.FundamentalWeight))
	}
	if data := ctx.SentimentData; data != nil {
		sb.WriteString("## 市场情绪\n\n")
		if data.FearGreedIndex > 0 {
			sb.WriteString(fmt.Sprintf("恐惧贪婪指数: %d/100 (%s)\n", data.FearGreedIndex, data.FearGreedLabel))
		}
		if data.PositiveRatio > 0 || data.NegativeRatio > 0 {
			sb.WriteString(fmt.Sprintf("CryptoRacle: 乐观 %.1f%% | 悲观 %.1f%% | 净值 %+.3f\n",
				data.PositiveRatio*100, data.NegativeRatio*100, data.NetSentiment))
		}
		sb.WriteString(fmt.Sprintf("数据来源: %s | 时间: %s\n\n", strings.Join(data.Sources, ", "), data.DataTime))
	}
	if data := ctx.ValuationData; data != nil {
		sb.WriteString(fmt.Sprintf("## 市场估值\n\n总市值: $%.0fB (24h %+.1f%%) | BTC 占比: %.1f%% | ETH 占比: %.1f%%\n\n",
			data.TotalMarketCap/1e9, data.MarketCapChange24h, data.BTCDominance, data.ETHDominance))
	}
	return sb.String()
}

func formatMultiFactorDataEN(ctx *Context) string {
	var sb strings.Builder
	if w := ctx.DecisionWeights; w != nil {
		sb.WriteString(fmt.Sprintf("## Decision Weights\n\nTechnical %d%% | Sentiment %d%% | Valuation %d%% | Fundamental %d%%\n\n",
			w.TechnicalWeight, w.SentimentWeight, w.ValuationWeight, w.FundamentalWeight))
	}
	if data := ctx.SentimentData; data != nil {
		sb.WriteString("## Market Sentiment\n\n")
		if data.FearGreedIndex > 0 {
			sb.WriteString(fmt.Sprintf("Fear & Greed Index: %d/100 (%s)\n", data.FearGreedIndex, data.FearGreedLabel))
		}
		if data.PositiveRatio > 0 || data.NegativeRatio > 0 {
			sb.WriteString(fmt.Sprintf("CryptoRacle: Positive %.1f%% | Negative %.1f%% | Net %+.3f\n",
				data.PositiveRatio*100, data.NegativeRatio*100, data.NetSentiment))
		}
		sb.WriteString(fmt.Sprintf("Sources: %s | Time: %s\n\n", strings.Join(data.Sources, ", "), data.DataTime))
	}
	if data := ctx.ValuationData; data != nil {
		sb.WriteString(fmt.Sprintf("## Market Valuation\n\nTotal market cap: $%.0fB (24h %+.1f%%) | BTC dominance: %.1f%% | ETH dominance: %.1f%%\n\n",
			data.TotalMarketCap/1e9, data.MarketCapChange24h, data.BTCDominance, data.ETHDominance))
	}
	return sb.String()
}
