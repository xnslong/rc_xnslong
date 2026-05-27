// Package logger 提供结构化日志初始化（zerolog）。
//
// 全局字段: ts, level, module, trace_id
// 采样策略: error/warn 全量, info 正常投递每 100 条采样 1 条, debug 仅开发环境
//
// TODO(Iteration 2): 实现 Init 函数
// TODO(Iteration 2): 实现 ModuleLogger 创建带 module 字段的子 logger
// TODO(Iteration 2): 实现采样配置
package logger

import (
	"os"
	"time"

	"github.com/rs/zerolog"
)

// Init 初始化全局日志配置。
func Init(level string) {
	lvl, err := zerolog.ParseLevel(level)
	if err != nil {
		lvl = zerolog.InfoLevel
	}

	output := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
	logger := zerolog.New(output).
		Level(lvl).
		With().
		Timestamp().
		Logger()

	zerolog.DefaultContextLogger = &logger
}

// Module 创建一个带 module 字段的日志实例。
func Module(name string) zerolog.Logger {
	return zerolog.DefaultContextLogger.With().Str("module", name).Logger()
}

// TODO(Iteration 2): 添加采样控制: info 级别的正常投递日志每 100 条采样 1 条
// 使用 zerolog.Sample 或自定义 sampler
