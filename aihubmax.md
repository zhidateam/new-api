# 本地测试方法

## 编译前端
```shell
cd web
pnpm install
pnpm run build
```

## 运行go（测试）

```shell
cd ..
go run main.go
```

# 测试项目

## 正常渠道使用

后台添加渠道
cherry studio测试模型，观察是否能正常流式输出
apifox调用，观察是否符合输出结构`返回数据结构与接口定义一致`


## 测试chanel_id显示

后台添加一个渠道，随便写域名和key
加一个新的模型`test`
观察channel_id