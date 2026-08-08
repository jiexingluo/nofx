package api

import (
	"fmt"
	"net/http"
	"nofx/crypto"
	"nofx/mcp"
	_ "nofx/mcp/payment"
	_ "nofx/mcp/provider"
	"nofx/store"

	"github.com/gin-gonic/gin"
)

type testModelConfigRequest struct {
	Provider        string `json:"provider" binding:"required"`
	APIKey          string `json:"api_key" binding:"required"`
	CustomAPIURL    string `json:"custom_api_url"`
	CustomModelName string `json:"custom_model_name"`
}

func (s *Server) handleTestModelConfig(c *gin.Context) {
	var req testModelConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	client := mcp.NewAIClientByProvider(req.Provider)
	if req.Provider == mcp.ProviderCustom {
		client = mcp.New()
	}
	if client == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unsupported model provider"})
		return
	}
	client.SetAPIKey(req.APIKey, req.CustomAPIURL, req.CustomModelName)
	if _, err := client.CallWithMessages("You are a connectivity probe.", "Reply with OK."); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Failed to connect: %v", err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Connection successful"})
}

type testExchangeConfigRequest struct {
	ExchangeType            string `json:"exchange_type" binding:"required"`
	APIKey                  string `json:"api_key"`
	SecretKey               string `json:"secret_key"`
	Passphrase              string `json:"passphrase"`
	Testnet                 bool   `json:"testnet"`
	HyperliquidWalletAddr   string `json:"hyperliquid_wallet_addr"`
	AsterUser               string `json:"aster_user"`
	AsterSigner             string `json:"aster_signer"`
	AsterPrivateKey         string `json:"aster_private_key"`
	LighterWalletAddr       string `json:"lighter_wallet_addr"`
	LighterAPIKeyPrivateKey string `json:"lighter_api_key_private_key"`
	LighterAPIKeyIndex      int    `json:"lighter_api_key_index"`
}

func (s *Server) handleTestExchangeConfig(c *gin.Context) {
	var req testExchangeConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	exchangeConfig := &store.Exchange{
		ExchangeType: req.ExchangeType,
		APIKey:       crypto.EncryptedString(req.APIKey), SecretKey: crypto.EncryptedString(req.SecretKey),
		Passphrase: crypto.EncryptedString(req.Passphrase), Testnet: req.Testnet,
		HyperliquidWalletAddr: req.HyperliquidWalletAddr, HyperliquidUnifiedAcct: true,
		AsterUser: req.AsterUser, AsterSigner: req.AsterSigner,
		AsterPrivateKey:         crypto.EncryptedString(req.AsterPrivateKey),
		LighterWalletAddr:       req.LighterWalletAddr,
		LighterAPIKeyPrivateKey: crypto.EncryptedString(req.LighterAPIKeyPrivateKey),
		LighterAPIKeyIndex:      req.LighterAPIKeyIndex,
	}
	probe, err := buildExchangeProbeTrader(exchangeConfig, c.GetString("user_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Failed to initialize exchange client: %v", err)})
		return
	}
	if _, err := probe.GetBalance(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Failed to fetch balance info: %v", err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Connection successful"})
}
