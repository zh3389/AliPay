package main

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/serverless-plus/tencent-serverless-go/events"
	"github.com/serverless-plus/tencent-serverless-go/faas"
)

// APIResponse 统一 JSON 响应
type APIResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

var (
	cfg     *Config
	initErr string // 初始化错误信息（非空表示初始化失败）
)

func main() {
	// 延迟初始化：配置/密钥出错时不崩溃，请求时返回错误 JSON，避免容器反复重启
	c, err := loadConfig()
	if err != nil {
		initErr = err.Error()
		log.Printf("[init] 配置加载失败: %v", err)
	} else {
		cfg = c
		alipay.appID = c.AppID
		alipay.privateKey = c.PrivateKey
		alipay.publicKey = c.PublicKey
		alipay.gateway = c.Gateway
	}
	faas.Start(handler)
}

// handler SCF 函数 URL 入口，按 path + method 路由
func handler(ctx context.Context, event events.APIGatewayRequest) (events.APIGatewayResponse, error) {
	if initErr != "" {
		if strings.TrimRight(event.Path, "/") == "/pay/health" {
			return errResp(500, "init failed: "+initErr), nil
		}
		return errResp(500, "服务未就绪，请查看 /pay/health"), nil
	}

	path := strings.TrimRight(event.Path, "/")
	method := strings.ToUpper(event.Method)

	switch {
	case path == "/pay/precreate" && method == "POST":
		return handlePrecreate(ctx, event)
	case path == "/pay/refund" && method == "POST":
		return handleRefund(ctx, event)
	case path == "/pay/notify" && method == "POST":
		return handleNotify(ctx, event)
	case path == "/pay/query" && method == "GET":
		return handleQuery(ctx, event)
	case path == "/pay/health":
		return okResp(map[string]any{
			"app_id":       alipay.appID,
			"gateway":      alipay.gateway,
			"seller_id":    cfg.SellerID,
			"notify_url":   cfg.NotifyURL,
			"has_priv_key": alipay.privateKey != nil,
			"has_pub_key":  alipay.publicKey != nil,
		}), nil
	default:
		return errResp(404, "not found"), nil
	}
}

// ---------- 请求结构体 ----------

type precreateReq struct {
	Description string `json:"description"`    // 商品描述
	OutTradeNo  string `json:"out_trade_no"`   // 商户订单号（可选，为空自动生成）
	Amount      int64  `json:"amount"`         // 金额，单位：分
}

type refundReq struct {
	OutTradeNo   string `json:"out_trade_no"`   // 原商户订单号
	OutRequestNo string `json:"out_request_no"` // 商户退款请求号（可选，为空自动生成）
	Reason       string `json:"reason"`         // 退款原因（可选）
	Refund       int64  `json:"refund"`         // 退款金额，单位：分
}

// ---------- 订单码支付下单 ----------

func handlePrecreate(ctx context.Context, event events.APIGatewayRequest) (events.APIGatewayResponse, error) {
	var req precreateReq
	if err := json.Unmarshal([]byte(event.Body), &req); err != nil {
		return errResp(400, "请求参数错误"), nil
	}
	if req.Description == "" || req.Amount <= 0 {
		return errResp(400, "description 和 amount 为必填"), nil
	}
	if req.OutTradeNo == "" {
		req.OutTradeNo = genOrderNo()
	}

	qrCode, err := alipay.precreate(req.OutTradeNo, req.Description, req.Amount, cfg.SellerID)
	if err != nil {
		log.Printf("订单码下单失败: %v", err)
		return errResp(500, "下单失败: "+err.Error()), nil
	}
	return okResp(map[string]any{
		"out_trade_no": req.OutTradeNo,
		"qr_code":      qrCode,
	}), nil
}

// ---------- 退款 ----------

func handleRefund(ctx context.Context, event events.APIGatewayRequest) (events.APIGatewayResponse, error) {
	var req refundReq
	if err := json.Unmarshal([]byte(event.Body), &req); err != nil {
		return errResp(400, "请求参数错误"), nil
	}
	if req.OutTradeNo == "" || req.Refund <= 0 {
		return errResp(400, "out_trade_no, refund 为必填"), nil
	}
	if req.OutRequestNo == "" {
		req.OutRequestNo = genOrderNo()
	}

	res, err := alipay.refund(req.OutTradeNo, req.Refund, req.Reason, req.OutRequestNo)
	if err != nil {
		log.Printf("退款失败: %v", err)
		return errResp(500, "退款失败: "+err.Error()), nil
	}
	return okResp(res), nil
}

// ---------- 异步通知验签 ----------

func handleNotify(ctx context.Context, event events.APIGatewayRequest) (events.APIGatewayResponse, error) {
	params, err := alipay.verifyNotify([]byte(event.Body))
	if err != nil {
		log.Printf("通知验签失败: %v", err)
		return plainResp(200, "failure"), nil
	}
	// 业务一致性校验 + 处理（示例仅记录日志，请在此更新订单状态、发货等）
	if params["trade_status"] != "TRADE_SUCCESS" && params["trade_status"] != "TRADE_FINISHED" {
		return plainResp(200, "success"), nil
	}
	log.Printf("支付成功: out_trade_no=%s trade_no=%s total_amount=%s buyer=%s",
		params["out_trade_no"], params["trade_no"], params["total_amount"], params["buyer_id"])
	return plainResp(200, "success"), nil
}

// ---------- 查询订单 ----------

func handleQuery(ctx context.Context, event events.APIGatewayRequest) (events.APIGatewayResponse, error) {
	outTradeNo := getQueryParam(event.QueryString, "out_trade_no")
	if outTradeNo == "" {
		return errResp(400, "out_trade_no 为必填"), nil
	}
	res, err := alipay.query(outTradeNo)
	if err != nil {
		log.Printf("查询订单失败: %v", err)
		return errResp(500, "查询失败: "+err.Error()), nil
	}
	return okResp(res), nil
}

// ---------- 响应辅助 ----------

func jsonResp(status int, data any) events.APIGatewayResponse {
	body, _ := json.Marshal(data)
	return events.APIGatewayResponse{
		StatusCode: status,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       string(body),
	}
}

func plainResp(status int, body string) events.APIGatewayResponse {
	return events.APIGatewayResponse{StatusCode: status, Body: body}
}

func okResp(data any) events.APIGatewayResponse {
	return jsonResp(200, APIResponse{Code: 0, Message: "ok", Data: data})
}

func errResp(status int, msg string) events.APIGatewayResponse {
	return jsonResp(status, APIResponse{Code: -1, Message: msg})
}

// ---------- 配置加载（全部从环境变量/文件读取，零硬编码） ----------

type Config struct {
	AppID      string
	Gateway    string
	NotifyURL  string
	SellerID   string
	PrivateKey *rsa.PrivateKey // 应用私钥
	PublicKey  *rsa.PublicKey  // 支付宝公钥
}

func loadConfig() (*Config, error) {
	c := &Config{
		AppID:     os.Getenv("ALIPAY_APP_ID"),
		Gateway:   os.Getenv("ALIPAY_GATEWAY"),
		NotifyURL: os.Getenv("ALIPAY_NOTIFY_URL"),
		SellerID:  os.Getenv("ALIPAY_SELLER_ID"),
	}
	if c.Gateway == "" {
		c.Gateway = "https://openapi-sandbox.dl.alipaydev.com/gateway.do" // 默认沙箱网关
	}
	var err error
	c.PrivateKey, err = loadPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("加载应用私钥失败: %w", err)
	}
	c.PublicKey, err = loadPublicKey()
	if err != nil {
		return nil, fmt.Errorf("加载支付宝公钥失败: %w", err)
	}
	if c.AppID == "" {
		return nil, fmt.Errorf("缺少环境变量: ALIPAY_APP_ID")
	}
	return c, nil
}

// loadPrivateKey 优先环境变量 ALIPAY_PRIVATE_KEY，其次文件路径 ALIPAY_PRIVATE_KEY_PATH（默认 keys/app_private_key.pem）
func loadPrivateKey() (*rsa.PrivateKey, error) {
	if pemStr := os.Getenv("ALIPAY_PRIVATE_KEY"); pemStr != "" {
		return parsePrivateKey([]byte(pemStr))
	}
	path := os.Getenv("ALIPAY_PRIVATE_KEY_PATH")
	if path == "" {
		path = "keys/app_private_key.pem"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取私钥文件失败: %w", err)
	}
	return parsePrivateKey(data)
}

func parsePrivateKey(data []byte) (*rsa.PrivateKey, error) {
	s := normalizePEM(string(data))
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return nil, fmt.Errorf("私钥 PEM 解码失败，内容长度=%d，前40字符=%q", len(s), truncate(s, 40))
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, fmt.Errorf("无法解析 RSA 私钥")
}

// loadPublicKey 优先环境变量 ALIPAY_PUBLIC_KEY，其次文件路径 ALIPAY_PUBLIC_KEY_PATH（默认 keys/alipay_public_key.pem）
func loadPublicKey() (*rsa.PublicKey, error) {
	if pemStr := os.Getenv("ALIPAY_PUBLIC_KEY"); pemStr != "" {
		return parsePublicKey([]byte(pemStr))
	}
	path := os.Getenv("ALIPAY_PUBLIC_KEY_PATH")
	if path == "" {
		path = "keys/alipay_public_key.pem"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取公钥文件失败: %w", err)
	}
	return parsePublicKey(data)
}

func parsePublicKey(data []byte) (*rsa.PublicKey, error) {
	s := normalizePEM(string(data))
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return nil, fmt.Errorf("公钥 PEM 解码失败，内容长度=%d，前40字符=%q", len(s), truncate(s, 40))
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析公钥失败: %w", err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("非 RSA 公钥")
	}
	return rsaPub, nil
}

// ---------- 工具函数 ----------

// normalizePEM 归一化 PEM 文本，处理环境变量中换行符丢失/转义的情况
func normalizePEM(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	s = strings.ReplaceAll(s, "\\n", "\n")
	s = strings.ReplaceAll(s, "\\r", "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	for _, marker := range []string{"-----BEGIN PRIVATE KEY-----", "-----BEGIN PUBLIC KEY-----", "-----BEGIN RSA PRIVATE KEY-----", "-----BEGIN CERTIFICATE-----"} {
		s = strings.ReplaceAll(s, marker, marker+"\n")
	}
	for _, marker := range []string{"-----END PRIVATE KEY-----", "-----END PUBLIC KEY-----", "-----END RSA PRIVATE KEY-----", "-----END CERTIFICATE-----"} {
		s = strings.ReplaceAll(s, marker, "\n"+marker)
	}
	for strings.Contains(s, "\n\n") {
		s = strings.ReplaceAll(s, "\n\n", "\n")
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// genOrderNo 生成商户订单号 / 退款请求号
func genOrderNo() string {
	return fmt.Sprintf("SCF%s%04d", time.Now().Format("20060102150405"), rand.Intn(10000))
}

// getQueryParam 从 API Gateway 查询参数中取第一个值
func getQueryParam(qs map[string][]string, key string) string {
	if vals, ok := qs[key]; ok && len(vals) > 0 {
		return vals[0]
	}
	return ""
}
