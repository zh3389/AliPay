#!/bin/bash
# 支付宝订单码支付完整测试脚本
# 用法: ./test.sh [health|precreate|query|refund]

BASE="https://<function-url>.ap-chengdu.tencentscf.com"
CMD=${1:-precreate}

echo "=========================================="
echo " 支付宝支付测试  ($CMD)"
echo "=========================================="

case "$CMD" in
  health)
    echo "→ 健康检查"
    curl -s "$BASE/pay/health" | python3 -m json.tool
    ;;

  precreate)
    echo "→ 订单码下单（1分钱）"
    RESP=$(curl -s -X POST "$BASE/pay/precreate" \
      -H "Content-Type: application/json" \
      -d '{"description":"测试商品-1分钱","amount":1}')
    echo "$RESP" | python3 -m json.tool

    QR_CODE=$(echo "$RESP" | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['qr_code'])" 2>/dev/null)
    TRADE_NO=$(echo "$RESP" | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['out_trade_no'])" 2>/dev/null)

    if [ -n "$QR_CODE" ]; then
      echo ""
      echo "✅ 下单成功！"
      echo "订单号: $TRADE_NO"
      echo ""
      echo "请用【沙箱支付宝 App】扫码支付（沙箱买家账号 xxxxxxxxxx@sandbox.com）"
      echo "或复制以下链接到浏览器显示二维码："
      echo "  https://api.qrserver.com/v1/create-qr-code/?size=300x300&data=$(python3 -c "import urllib.parse; print(urllib.parse.quote('$QR_CODE'))")"
      echo ""
      echo "支付完成后查询订单:"
      echo "  ./test.sh query $TRADE_NO"
    fi
    ;;

  query)
    TRADE_NO=${2:-""}
    if [ -z "$TRADE_NO" ]; then
      echo "用法: ./test.sh query <out_trade_no>"
      exit 1
    fi
    echo "→ 查询订单: $TRADE_NO"
    curl -s "$BASE/pay/query?out_trade_no=$TRADE_NO" | python3 -m json.tool
    ;;

  refund)
    TRADE_NO=${2:-""}
    if [ -z "$TRADE_NO" ]; then
      echo "用法: ./test.sh refund <out_trade_no> [金额分]"
      echo "示例: ./test.sh refund SCF202608141015074888 1"
      exit 1
    fi
    REFUND=${3:-1}
    echo "→ 退款: 订单=$TRADE_NO 退款金额=${REFUND}分"
    curl -s -X POST "$BASE/pay/refund" \
      -H "Content-Type: application/json" \
      -d "{\"out_trade_no\":\"$TRADE_NO\",\"refund\":$REFUND,\"reason\":\"测试退款\"}" | python3 -m json.tool
    ;;

  *)
    echo "用法: ./test.sh [health|precreate|query|refund]"
    echo ""
    echo "  ./test.sh health                 健康检查"
    echo "  ./test.sh precreate              订单码下单(扫码)"
    echo "  ./test.sh query <订单号>          查询订单"
    echo "  ./test.sh refund <订单号> [金额分]  退款"
    ;;
esac
