.PHONY: build clean tidy

# 编译为 Linux amd64 二进制并打包 zip（SCF 要求）
build:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o main .
	zip -r main.zip main

# 同时打包密钥文件（本地测试用；生产环境建议用环境变量）
build-with-keys: build
	zip -r main.zip main keys/

# 整理依赖
tidy:
	go mod tidy

clean:
	rm -f main main.zip
