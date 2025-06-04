package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"one-api/common"
	constant2 "one-api/constant"
	"one-api/dto"
	"one-api/middleware"
	"one-api/model"
	"one-api/relay"
	"one-api/relay/constant"
	relayconstant "one-api/relay/constant"
	"one-api/relay/helper"
	"one-api/service"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func relayHandler(c *gin.Context, relayMode int) *dto.OpenAIErrorWithStatusCode {
	var err *dto.OpenAIErrorWithStatusCode
	switch relayMode {
	case relayconstant.RelayModeImagesGenerations, relayconstant.RelayModeImagesEdits:
		err = relay.ImageHelper(c)
	case relayconstant.RelayModeAudioSpeech:
		fallthrough
	case relayconstant.RelayModeAudioTranslation:
		fallthrough
	case relayconstant.RelayModeAudioTranscription:
		err = relay.AudioHelper(c)
	case relayconstant.RelayModeRerank:
		err = relay.RerankHelper(c, relayMode)
	case relayconstant.RelayModeEmbeddings:
		err = relay.EmbeddingHelper(c)
	case relayconstant.RelayModeResponses:
		err = relay.ResponsesHelper(c)
	case relayconstant.RelayModeGemini:
		err = relay.GeminiHelper(c)
	default:
		err = relay.TextHelper(c)
	}

	if constant2.ErrorLogEnabled && err != nil {
		// 保存错误日志到mysql中
		userId := c.GetInt("id")
		tokenName := c.GetString("token_name")
		modelName := c.GetString("original_model")
		tokenId := c.GetInt("token_id")
		userGroup := c.GetString("group")
		channelId := c.GetInt("channel_id")
		other := make(map[string]interface{})
		other["error_type"] = err.Error.Type
		other["error_code"] = err.Error.Code
		other["status_code"] = err.StatusCode
		other["channel_id"] = channelId
		other["channel_name"] = c.GetString("channel_name")
		other["channel_type"] = c.GetInt("channel_type")

		model.RecordErrorLog(c, userId, channelId, modelName, tokenName, err.Error.Message, tokenId, 0, false, userGroup, other)
	}

	return err
}

func Relay(c *gin.Context) {
	relayMode := constant.Path2RelayMode(c.Request.URL.Path)
	requestId := c.GetString(common.RequestIdKey)
	group := c.GetString("group")
	originalModel := c.GetString("original_model")
	var openaiErr *dto.OpenAIErrorWithStatusCode
	//aihubmax begin
	var lastChannelId int

	for i := 0; i <= common.RetryTimes; i++ {
		channel, err := getChannel(c, group, originalModel, i)
		if err != nil {
			common.LogError(c, err.Error())
			openaiErr = service.OpenAIErrorWrapperLocal(err, "get_channel_failed", http.StatusInternalServerError)
			break
		}
		lastChannelId = channel.Id

		openaiErr = relayRequest(c, relayMode, channel)

		if openaiErr == nil {
			return // 成功处理请求，直接返回
		}

		_ = processChannelError(c, channel.Id, channel.Type, channel.Name, channel.GetAutoBan(), openaiErr)

		if !shouldRetry(c, openaiErr, common.RetryTimes-i) {
			break
		}
	}
	// 构建错误信息字符串
	errorMsgs := []string{}
	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		// 打印所有的context keys以便调试
		common.LogInfo(c, fmt.Sprintf("正在获取错误信息，useChannel: %v", useChannel))
		for _, chIdStr := range useChannel {
			chId, _ := strconv.Atoi(chIdStr)
			key := fmt.Sprintf("channel_error_%d", chId)
			common.LogInfo(c, fmt.Sprintf("检查key: %s", key))
			if errMsg, exists := c.Get(key); exists {
				common.LogInfo(c, fmt.Sprintf("找到错误信息: %s", errMsg.(string)))
				errorMsgs = append(errorMsgs, errMsg.(string))
			} else {
				common.LogInfo(c, fmt.Sprintf("未找到key: %s的错误信息", key))
			}
		}

		retryLogStr := fmt.Sprintf("重试：%s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		common.LogInfo(c, retryLogStr)
		if len(errorMsgs) > 0 {
			retryLogStr = fmt.Sprintf("%s\n汇总错误信息：%s", retryLogStr, strings.Join(errorMsgs, "\n"))
		}
		common.LogInfo(c, retryLogStr)
	}

	if openaiErr != nil {
		if openaiErr.StatusCode == http.StatusTooManyRequests {
			common.LogError(c, fmt.Sprintf("origin 429 error: %s", openaiErr.Error.Message))
			openaiErr.Error.Message = "当前分组上游负载已饱和，请稍后再试"
		}

		// 打印错误信息
		common.LogError(c, fmt.Sprintf("local_error: %v", openaiErr.LocalError))
		if !openaiErr.LocalError {
			openaiErr.Error.UpstreamError = 1
		}

		// 在所有错误响应中添加 channelId
		openaiErr.Error.ChannelId = lastChannelId

		openaiErr.Error.Message = common.MessageWithRequestId(openaiErr.Error.Message, requestId)
		c.JSON(openaiErr.StatusCode, gin.H{
			"error": openaiErr.Error,
		})

		fmtErrMsg := fmt.Sprintf("relay error (channel #%d, status code: %d): %s", lastChannelId, openaiErr.StatusCode, openaiErr.Error.Message)
		errorMsgs = append(errorMsgs, fmtErrMsg)
	}

	// Send retry fail log asynchronously
	sendRetryFailLog(c, originalModel, errorMsgs)
	//aihubmax end
}

var upgrader = websocket.Upgrader{
	Subprotocols: []string{"realtime"}, // WS 握手支持的协议，如果有使用 Sec-WebSocket-Protocol，则必须在此声明对应的 Protocol TODO add other protocol
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许跨域
	},
}

func WssRelay(c *gin.Context) {
	// 将 HTTP 连接升级为 WebSocket 连接

	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	defer ws.Close()

	if err != nil {
		openaiErr := service.OpenAIErrorWrapper(err, "get_channel_failed", http.StatusInternalServerError)
		helper.WssError(c, ws, openaiErr.Error)
		return
	}

	relayMode := constant.Path2RelayMode(c.Request.URL.Path)
	requestId := c.GetString(common.RequestIdKey)
	group := c.GetString("group")
	//wss://api.openai.com/v1/realtime?model=gpt-4o-realtime-preview-2024-10-01
	originalModel := c.GetString("original_model")
	var openaiErr *dto.OpenAIErrorWithStatusCode
	//aihubmax begin
	for i := 0; i <= common.RetryTimes; i++ {
		channel, err := getChannel(c, group, originalModel, i)
		if err != nil {
			common.LogError(c, err.Error())
			openaiErr = service.OpenAIErrorWrapperLocal(err, "get_channel_failed", http.StatusInternalServerError)
			break
		}

		openaiErr = wssRequest(c, ws, relayMode, channel)

		if openaiErr == nil {
			return // 成功处理请求，直接返回
		}

		_ = processChannelError(c, channel.Id, channel.Type, channel.Name, channel.GetAutoBan(), openaiErr)

		if !shouldRetry(c, openaiErr, common.RetryTimes-i) {
			break
		}
	}
	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		// 构建错误信息字符串
		errorMsgs := []string{}
		for _, chIdStr := range useChannel {
			chId, _ := strconv.Atoi(chIdStr)
			key := fmt.Sprintf("channel_error_%d", chId)
			if errMsg, exists := c.Get(key); exists {
				errorMsgs = append(errorMsgs, errMsg.(string))
			}
		}

		retryLogStr := fmt.Sprintf("重试：%s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		if len(errorMsgs) > 0 {
			retryLogStr = fmt.Sprintf("%s\n错误信息：%s", retryLogStr, strings.Join(errorMsgs, "\n"))
		}
		common.LogInfo(c, retryLogStr)
	}
	//aihubmax end

	if openaiErr != nil {
		if openaiErr.StatusCode == http.StatusTooManyRequests {
			openaiErr.Error.Message = "当前分组上游负载已饱和，请稍后再试"
		}
		openaiErr.Error.Message = common.MessageWithRequestId(openaiErr.Error.Message, requestId)
		helper.WssError(c, ws, openaiErr.Error)
	}
}

func RelayClaude(c *gin.Context) {
	requestId := c.GetString(common.RequestIdKey)
	group := c.GetString("group")
	originalModel := c.GetString("original_model")
	var claudeErr *dto.ClaudeErrorWithStatusCode
	//aihubmax begin
	var lastChannelId int

	for i := 0; i <= common.RetryTimes; i++ {
		channel, err := getChannel(c, group, originalModel, i)
		if err != nil {
			common.LogError(c, err.Error())
			claudeErr = service.ClaudeErrorWrapperLocal(err, "get_channel_failed", http.StatusInternalServerError)
			break
		}
		lastChannelId = channel.Id

		claudeErr = claudeRequest(c, channel)

		if claudeErr == nil {
			return // 成功处理请求，直接返回
		}

		openaiErr := service.ClaudeErrorToOpenAIError(claudeErr)

		_ = processChannelError(c, channel.Id, channel.Type, channel.Name, channel.GetAutoBan(), openaiErr)

		if !shouldRetry(c, openaiErr, common.RetryTimes-i) {
			break
		}
	}
	// 构建错误信息字符串
	errorMsgs := []string{}
	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		for _, chIdStr := range useChannel {
			chId, _ := strconv.Atoi(chIdStr)
			key := fmt.Sprintf("channel_error_%d", chId)
			if errMsg, exists := c.Get(key); exists {
				errorMsgs = append(errorMsgs, errMsg.(string))
			}
		}

		retryLogStr := fmt.Sprintf("重试：%s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		if len(errorMsgs) > 0 {
			retryLogStr = fmt.Sprintf("%s\n错误信息：%s", retryLogStr, strings.Join(errorMsgs, "\n"))
		}
		common.LogInfo(c, retryLogStr)
	}

	if claudeErr != nil {
		// 在所有错误响应中添加 channelId
		claudeErr.Error.ChannelId = lastChannelId
		claudeErr.Error.Message = common.MessageWithRequestId(claudeErr.Error.Message, requestId)
		c.JSON(claudeErr.StatusCode, gin.H{
			"type":  "error",
			"error": claudeErr.Error,
		})

		fmtErrMsg := fmt.Sprintf("relay error (channel #%d, status code: %d): %s", lastChannelId, claudeErr.StatusCode, claudeErr.Error.Message)
		errorMsgs = append(errorMsgs, fmtErrMsg)
	}

	// Send retry fail log asynchronously
	sendRetryFailLog(c, originalModel, errorMsgs)
	//aihubmax end
}

func relayRequest(c *gin.Context, relayMode int, channel *model.Channel) *dto.OpenAIErrorWithStatusCode {
	addUsedChannel(c, channel.Id)
	requestBody, _ := common.GetRequestBody(c)
	c.Request.Body = io.NopCloser(bytes.NewBuffer(requestBody))
	return relayHandler(c, relayMode)
}

func wssRequest(c *gin.Context, ws *websocket.Conn, relayMode int, channel *model.Channel) *dto.OpenAIErrorWithStatusCode {
	addUsedChannel(c, channel.Id)
	requestBody, _ := common.GetRequestBody(c)
	c.Request.Body = io.NopCloser(bytes.NewBuffer(requestBody))
	return relay.WssHelper(c, ws)
}

func claudeRequest(c *gin.Context, channel *model.Channel) *dto.ClaudeErrorWithStatusCode {
	addUsedChannel(c, channel.Id)
	requestBody, _ := common.GetRequestBody(c)
	c.Request.Body = io.NopCloser(bytes.NewBuffer(requestBody))
	return relay.ClaudeHelper(c)
}

func addUsedChannel(c *gin.Context, channelId int) {
	useChannel := c.GetStringSlice("use_channel")
	useChannel = append(useChannel, fmt.Sprintf("%d", channelId))
	c.Set("use_channel", useChannel)
}

func getChannel(c *gin.Context, group, originalModel string, retryCount int) (*model.Channel, error) {
	if retryCount == 0 {
		autoBan := c.GetBool("auto_ban")
		autoBanInt := 1
		if !autoBan {
			autoBanInt = 0
		}
		return &model.Channel{
			Id:      c.GetInt("channel_id"),
			Type:    c.GetInt("channel_type"),
			Name:    c.GetString("channel_name"),
			AutoBan: &autoBanInt,
		}, nil
	}
	channel, err := model.CacheGetRandomSatisfiedChannel(group, originalModel, retryCount)
	if err != nil {
		return nil, errors.New(fmt.Sprintf("获取重试渠道失败: %s", err.Error()))
	}
	middleware.SetupContextForSelectedChannel(c, channel, originalModel)
	return channel, nil
}

func shouldRetry(c *gin.Context, openaiErr *dto.OpenAIErrorWithStatusCode, retryTimes int) bool {
	if openaiErr == nil {
		return false
	}
	if openaiErr.LocalError {
		return false
	}
	if retryTimes <= 0 {
		return false
	}
	if _, ok := c.Get("specific_channel_id"); ok {
		return false
	}
	if openaiErr.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if openaiErr.StatusCode == 307 {
		return true
	}
	if openaiErr.StatusCode/100 == 5 {
		// 超时不重试
		if openaiErr.StatusCode == 504 || openaiErr.StatusCode == 524 {
			return false
		}
		return true
	}
	if openaiErr.StatusCode == http.StatusBadRequest {
		channelType := c.GetInt("channel_type")
		if channelType == common.ChannelTypeAnthropic {
			return true
		}
		return false
	}
	if openaiErr.StatusCode == 408 {
		// azure处理超时不重试
		return false
	}
	if openaiErr.StatusCode/100 == 2 {
		return false
	}
	return true
}

// aihubmax
func processChannelError(c *gin.Context, channelId int, channelType int, channelName string, autoBan bool, err *dto.OpenAIErrorWithStatusCode) string {
	// 不要使用context获取渠道信息，异步处理时可能会出现渠道信息不一致的情况
	// do not use context to get channel info, there may be inconsistent channel info when processing asynchronously
	errorMsg := fmt.Sprintf("relay error (channel #%d, status code: %d): %s", channelId, err.StatusCode, err.Error.Message)
	common.LogError(c, errorMsg)
	// 将错误信息直接存储到context中
	key := fmt.Sprintf("channel_error_%d", channelId)
	common.LogInfo(c, fmt.Sprintf("存储错误信息到key: %s, 错误信息: %s", key, errorMsg))
	c.Set(key, errorMsg)
	if service.ShouldDisableChannel(channelType, err) && autoBan {
		service.DisableChannel(channelId, channelName, err.Error.Message)
	}
	return errorMsg
}

// aihubmax begin
func RelayMidjourney(c *gin.Context) {
	relayMode := c.GetInt("relay_mode")
	var err *dto.MidjourneyResponse
	switch relayMode {
	case relayconstant.RelayModeMidjourneyNotify:
		err = relay.RelayMidjourneyNotify(c)
	case relayconstant.RelayModeMidjourneyTaskFetch, relayconstant.RelayModeMidjourneyTaskFetchByCondition:
		err = relay.RelayMidjourneyTask(c, relayMode)
	case relayconstant.RelayModeMidjourneyTaskImageSeed:
		err = relay.RelayMidjourneyTaskImageSeed(c)
	case relayconstant.RelayModeSwapFace:
		err = relay.RelaySwapFace(c)
	default:
		err = relay.RelayMidjourneySubmit(c, relayMode)
	}
	//err = relayMidjourneySubmit(c, relayMode)
	log.Println(err)
	if err != nil {
		statusCode := http.StatusBadRequest
		if err.Code == 30 {
			err.Result = "当前分组负载已饱和，请稍后再试，或升级账户以提升服务质量。"
			statusCode = http.StatusTooManyRequests
		}
		c.JSON(statusCode, gin.H{
			"description": fmt.Sprintf("%s %s", err.Description, err.Result),
			"type":        "upstream_error",
			"code":        err.Code,
		})
		channelId := c.GetInt("channel_id")
		common.LogError(c, fmt.Sprintf("relay error (channel #%d, status code %d): %s", channelId, statusCode, fmt.Sprintf("%s %s", err.Description, err.Result)))
	}
}

func RelayNotImplemented(c *gin.Context) {
	err := dto.OpenAIError{
		Message: "API not implemented",
		Type:    "new_api_error",
		Param:   "",
		Code:    "api_not_implemented",
	}
	c.JSON(http.StatusNotImplemented, gin.H{
		"error": err,
	})
}

func RelayNotFound(c *gin.Context) {
	err := dto.OpenAIError{
		Message: fmt.Sprintf("Invalid URL (%s %s)", c.Request.Method, c.Request.URL.Path),
		Type:    "invalid_request_error",
		Param:   "",
		Code:    "",
	}
	c.JSON(http.StatusNotFound, gin.H{
		"error": err,
	})
}

func RelayTask(c *gin.Context) {
	retryTimes := common.RetryTimes
	channelId := c.GetInt("channel_id")
	relayMode := c.GetInt("relay_mode")
	group := c.GetString("group")
	originalModel := c.GetString("original_model")
	c.Set("use_channel", []string{fmt.Sprintf("%d", channelId)})
	taskErr := taskRelayHandler(c, relayMode)
	if taskErr == nil {
		retryTimes = 0
	} else {
		// 如果有错误，存储错误信息
		errorMsg := fmt.Sprintf("relay error (channel #%d, status code: %d): %s", channelId, taskErr.StatusCode, taskErr.Message)
		common.LogError(c, errorMsg)
		key := fmt.Sprintf("channel_error_%d", channelId)
		common.LogInfo(c, fmt.Sprintf("存储错误信息到key: %s, 错误信息: %s", key, errorMsg))
		c.Set(key, errorMsg)
	}
	for i := 0; shouldRetryTaskRelay(c, channelId, taskErr, retryTimes) && i < retryTimes; i++ {
		channel, err := model.CacheGetRandomSatisfiedChannel(group, originalModel, i)
		if err != nil {
			common.LogError(c, fmt.Sprintf("CacheGetRandomSatisfiedChannel failed: %s", err.Error()))
			break
		}
		channelId = channel.Id
		useChannel := c.GetStringSlice("use_channel")
		useChannel = append(useChannel, fmt.Sprintf("%d", channelId))
		c.Set("use_channel", useChannel)
		common.LogInfo(c, fmt.Sprintf("using channel #%d to retry (remain times %d)", channel.Id, i))
		middleware.SetupContextForSelectedChannel(c, channel, originalModel)

		requestBody, _ := common.GetRequestBody(c)
		c.Request.Body = io.NopCloser(bytes.NewBuffer(requestBody))
		taskErr = taskRelayHandler(c, relayMode)

		// 如果有错误，存储错误信息
		if taskErr != nil {
			errorMsg := fmt.Sprintf("relay error (channel #%d, status code: %d): %s", channelId, taskErr.StatusCode, taskErr.Message)
			common.LogError(c, errorMsg)
			key := fmt.Sprintf("channel_error_%d", channelId)
			common.LogInfo(c, fmt.Sprintf("存储错误信息到key: %s, 错误信息: %s", key, errorMsg))
			c.Set(key, errorMsg)
		}
	}
	// 构建错误信息字符串
	errorMsgs := []string{}
	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		for _, chIdStr := range useChannel {
			chId, _ := strconv.Atoi(chIdStr)
			key := fmt.Sprintf("channel_error_%d", chId)
			if errMsg, exists := c.Get(key); exists {
				errorMsgs = append(errorMsgs, errMsg.(string))
			}
		}

		retryLogStr := fmt.Sprintf("重试：%s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		if len(errorMsgs) > 0 {
			retryLogStr = fmt.Sprintf("%s\n错误信息：%s", retryLogStr, strings.Join(errorMsgs, "\n"))
		}
		common.LogInfo(c, retryLogStr)
	}
	if taskErr != nil {
		if taskErr.StatusCode == http.StatusTooManyRequests {
			taskErr.Message = "当前分组上游负载已饱和，请稍后再试"
		}
		c.JSON(taskErr.StatusCode, taskErr)

		fmtErrMsg := fmt.Sprintf("relay error (channel #%d, status code: %d): %s", channelId, taskErr.StatusCode, taskErr.Message)
		errorMsgs = append(errorMsgs, fmtErrMsg)
	}
	// Send retry fail log asynchronously
	sendRetryFailLog(c, originalModel, errorMsgs)
}

//aihubmax end

func taskRelayHandler(c *gin.Context, relayMode int) *dto.TaskError {
	var err *dto.TaskError
	switch relayMode {
	case relayconstant.RelayModeSunoFetch, relayconstant.RelayModeSunoFetchByID:
		err = relay.RelayTaskFetch(c, relayMode)
	default:
		err = relay.RelayTaskSubmit(c, relayMode)
	}
	return err
}

func shouldRetryTaskRelay(c *gin.Context, channelId int, taskErr *dto.TaskError, retryTimes int) bool {
	if taskErr == nil {
		return false
	}
	if retryTimes <= 0 {
		return false
	}
	if _, ok := c.Get("specific_channel_id"); ok {
		return false
	}
	if taskErr.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if taskErr.StatusCode == 307 {
		return true
	}
	if taskErr.StatusCode/100 == 5 {
		// 超时不重试
		if taskErr.StatusCode == 504 || taskErr.StatusCode == 524 {
			return false
		}
		return true
	}
	if taskErr.StatusCode == http.StatusBadRequest {
		return false
	}
	if taskErr.StatusCode == 408 {
		// azure处理超时不重试
		return false
	}
	if taskErr.LocalError {
		return false
	}
	if taskErr.StatusCode/100 == 2 {
		return false
	}
	return true
}

// aihubmax发送失败记录接口
func sendRetryFailLog(c *gin.Context, model string, errorMsgs []string) {
	go func() {
		// Get ahm_session_id from header if exists
		ahmSessionID := c.GetHeader("ahm_session_id")

		// Prepare request body
		requestBody := map[string]string{
			"model":          model,
			"errmsg":         strings.Join(errorMsgs, "|"),
			"ahm_session_id": ahmSessionID,
		}

		// Convert to JSON
		jsonData, err := json.Marshal(requestBody)
		if err != nil {
			common.LogError(c, fmt.Sprintf("sendRetryFailLog序列化重试失败日志请求失败: %v", err))
			return
		}

		// Get API URL from environment variable
		apiURL := os.Getenv("RETRY_FAIL_LOG_URL")
		if apiURL == "" {
			apiURL = "https://aihubmax.com/ext/retry_fail_log"
			common.LogInfo(c, "sendRetryFailLog未设置RETRY_FAIL_LOG_URL环境变量，使用默认值: "+apiURL)
		}

		// Make HTTP request
		resp, err := http.Post(apiURL, "application/json", bytes.NewBuffer(jsonData))
		if err != nil {
			common.LogError(c, fmt.Sprintf("sendRetryFailLog发送重试失败日志失败: %v", err))
			return
		}
		defer resp.Body.Close()

		common.LogInfo(c, fmt.Sprintf("sendRetryFailLog重试失败日志API返回状态码: %d", resp.StatusCode))
		if resp.StatusCode != http.StatusOK {
			common.LogError(c, fmt.Sprintf("sendRetryFailLog重试失败日志API返回非200状态码: %d", resp.StatusCode))
		}
	}()
}
