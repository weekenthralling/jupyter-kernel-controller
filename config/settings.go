package config

import (
	"log/slog"
	"os"

	"github.com/spf13/viper"
)

type Config struct {
	KernelShellPort   int `mapstructure:"kernel_shell_port"`
	KernelIOPubPort   int `mapstructure:"kernel_iopub_port"`
	KernelStdinPort   int `mapstructure:"kernel_stdin_port"`
	KernelHBPort      int `mapstructure:"kernel_hb_port"`
	KernelControlPort int `mapstructure:"kernel_control_port"`
}

func LoadConfig() *Config {

	// Enable Viper to read configuration from environment variables
	viper.AutomaticEnv()

	// Set default values
	viper.SetDefault("kernel_shell_port", "52317")
	viper.SetDefault("kernel_iopub_port", "52318")
	viper.SetDefault("kernel_hb_port", "52319")
	viper.SetDefault("kernel_control_port", "52320")
	viper.SetDefault("kernel_stdin_port", "52321")

	// Bind environment variables to Viper keys
	viper.BindEnv("kernel_shell_port")
	viper.BindEnv("kernel_iopub_port")
	viper.BindEnv("kernel_hb_port")
	viper.BindEnv("kernel_control_port")
	viper.BindEnv("kernel_stdin_port")

	var config Config
	// Read environment variables and decode into the Config struct
	if err := viper.Unmarshal(&config); err != nil {
		slog.Error("Error unmarshalling config", "error", err)
		os.Exit(1)
	}
	slog.Info("Config details", "config", config)
	return &config
}
