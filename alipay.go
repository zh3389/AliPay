package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// ---------- RSA2 签名/验签（支付宝官方标准） ----------

// signRSA2 使用应用私钥对 content 做 RSA2（SHA256withRSA）签名
func signRSA2(content string, priv *rsa.PrivateKey) (string, error) {
	hashed := sha256.Sum256([]byte(content))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, hashed[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// verifyRSA2 使用支付宝公钥验签（RSA2）
func verifyRSA2(content, signB64 string, pub *rsa.PublicKey) bool {
	sig, err := base64.StdEncoding.DecodeString(signB64)
	if err != nil {
		return false
	}
	hashed := sha256.Sum256([]byte(content))
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, hashed[:], sig) == nil
}

// marshalJSONEscaped 序列化为 JSON 并把非 ASCII 字符转义为 \uXXXX。
// 兼容支付宝网关对部分接口（如退款）biz_content 的严格验签：网关会把中文按 Java
// JSON 库默认行为转义为 \uXXXX 后重新拼串验签，Go 默认的 UTF-8 原样输出会导致验签失败。
func marshalJSONEscaped(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	s := string(b)
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			out.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		fmt.Fprintf(&out, `\u%04x`, r)
		i += size
	}
	return []byte(out.String()), nil
}

// buildSignContent 参数按 key 字典序拼接为 k1=v1&k2=v2，跳过空值
func buildSignContent(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(params[k])
	}
	return b.String()
}

// ---------- 支付宝客户端 ----------

type alipayClient struct {
	appID      string
	privateKey *rsa.PrivateKey // 应用私钥（请求加签）
	publicKey  *rsa.PublicKey  // 支付宝公钥（响应/通知验签）
	gateway    string
}

var alipay = &alipayClient{}

// request 通用调用：组装公共参数 → RSA2 加签 → POST form → 验签 → 解析业务结果
func (a *alipayClient) request(method string, bizContent map[string]any, result any) error {
	biz, err := marshalJSONEscaped(bizContent)
	if err != nil {
		return err
	}
	params := map[string]string{
		"app_id":      a.appID,
		"method":      method,
		"format":      "JSON",
		"charset":     "utf-8",
		"sign_type":   "RSA2",
		"timestamp":   time.Now().Format("2006-01-02 15:04:05"),
		"version":     "1.0",
		"biz_content": string(biz),
	}
	sign, err := signRSA2(buildSignContent(params), a.privateKey)
	if err != nil {
		return fmt.Errorf("加签失败: %w", err)
	}
	params["sign"] = sign

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	resp, err := http.PostForm(a.gateway, form)
	if err != nil {
		return fmt.Errorf("请求网关失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return a.parseResponse(method, body, result)
}

// parseResponse 解析网关响应：先查网关级错误，再验签，再查业务码
func (a *alipayClient) parseResponse(method string, body []byte, result any) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}
	if errResp, ok := m["error_response"]; ok {
		var e struct {
			Code    string `json:"code"`
			Msg     string `json:"msg"`
			SubCode string `json:"sub_code"`
			SubMsg  string `json:"sub_msg"`
		}
		json.Unmarshal(errResp, &e)
		return fmt.Errorf("支付宝网关错误: %s %s（%s %s）", e.Code, e.Msg, e.SubCode, e.SubMsg)
	}
	key := strings.ReplaceAll(method, ".", "_") + "_response"
	raw, ok := m[key]
	if !ok {
		return fmt.Errorf("响应缺少节点 %s", key)
	}
	// 验签：content = 业务响应节点的原始 JSON 值（保留 \/ 转义，不带节点名前缀，不做 URL 解码）
	if signRaw, ok := m["sign"]; ok && len(signRaw) > 0 {
		sign := strings.Trim(string(signRaw), `"`)
		content := string(raw)
		if !verifyRSA2(content, sign, a.publicKey) {
			return fmt.Errorf("响应验签失败（请检查支付宝公钥是否与当前环境匹配）")
		}
	}
	// 业务错误判断
	var biz struct {
		Code    string `json:"code"`
		Msg     string `json:"msg"`
		SubCode string `json:"sub_code"`
		SubMsg  string `json:"sub_msg"`
	}
	json.Unmarshal(raw, &biz)
	if biz.Code != "" && biz.Code != "10000" {
		return fmt.Errorf("支付宝业务错误: code=%s msg=%s sub_code=%s sub_msg=%s", biz.Code, biz.Msg, biz.SubCode, biz.SubMsg)
	}
	if result != nil {
		return json.Unmarshal(raw, result)
	}
	return nil
}

// ---------- 业务接口 ----------

// precreate 订单码支付下单，返回二维码内容
func (a *alipayClient) precreate(outTradeNo, subject string, amountFen int64, sellerID string) (string, error) {
	biz := map[string]any{
		"out_trade_no": outTradeNo,
		"total_amount": fen2yuan(amountFen),
		"subject":      subject,
		"product_code": "QR_CODE_OFFLINE", // 当面付固定产品码
	}
	if sellerID != "" {
		biz["seller_id"] = sellerID
	}
	var res struct {
		QRCode string `json:"qr_code"`
	}
	if err := a.request("alipay.trade.precreate", biz, &res); err != nil {
		return "", err
	}
	return res.QRCode, nil
}

// query 查询订单
func (a *alipayClient) query(outTradeNo string) (map[string]any, error) {
	var res map[string]any
	// 注意：沙箱环境查询必须显式携带 trade_no 字段（即使为空），否则返回 ACQ.TRADE_NOT_EXIST
	err := a.request("alipay.trade.query", map[string]any{"out_trade_no": outTradeNo, "trade_no": ""}, &res)
	return res, err
}

// refund 退款（支持部分退款）
func (a *alipayClient) refund(outTradeNo string, refundFen int64, reason, outRequestNo string) (map[string]any, error) {
	biz := map[string]any{
		"out_trade_no":   outTradeNo,
		"refund_amount":  fen2yuan(refundFen),
		"out_request_no": outRequestNo,
	}
	if reason != "" {
		biz["refund_reason"] = reason
	}
	var res map[string]any
	err := a.request("alipay.trade.refund", biz, &res)
	return res, err
}

// verifyNotify 异步通知验签，返回通知参数（值已 URLDecode）
func (a *alipayClient) verifyNotify(body []byte) (map[string]string, error) {
	vals, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	params := map[string]string{}
	for k, v := range vals {
		if k == "sign" || k == "sign_type" {
			continue
		}
		params[k] = v[0]
	}
	sign := vals.Get("sign")
	if sign == "" {
		return nil, fmt.Errorf("通知缺少 sign")
	}
	if !verifyRSA2(buildSignContent(params), sign, a.publicKey) {
		return nil, fmt.Errorf("通知验签失败")
	}
	return params, nil
}

// fen2yuan 分 → 元（支付宝金额单位为元）
func fen2yuan(f int64) string {
	return fmt.Sprintf("%d.%02d", f/100, f%100)
}
