package config

import (
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

type Config struct {
	KernelShellPort      int `mapstructure:"kernel_shell_port"`
	KernelIOPubPort      int `mapstructure:"kernel_iopub_port"`
	KernelStdinPort      int `mapstructure:"kernel_stdin_port"`
	KernelHBPort         int `mapstructure:"kernel_hb_port"`
	KernelControlPort    int `mapstructure:"kernel_control_port"`
	KernelCommSocketPort int `mapstructure:"kernel_comm_socket_port"`
}

func LoadConfig() *Config {

	// Enable Viper to read configuration from environment variables
	viper.AutomaticEnv()

	// Set default values
	viper.SetDefault("kernel_shell_port", "52700")
	viper.SetDefault("kernel_iopub_port", "52701")
	viper.SetDefault("kernel_hb_port", "52702")
	viper.SetDefault("kernel_control_port", "52703")
	viper.SetDefault("kernel_stdin_port", "52704")
	viper.SetDefault("kernel_comm_socket_port", "52705")

	// Bind environment variables to Viper keys
	viper.BindEnv("kernel_shell_port")
	viper.BindEnv("kernel_iopub_port")
	viper.BindEnv("kernel_hb_port")
	viper.BindEnv("kernel_control_port")
	viper.BindEnv("kernel_stdin_port")
	viper.BindEnv("kernel_comm_socket_port")

	var config Config
	// Read environment variables and decode into the Config struct
	if err := viper.Unmarshal(&config); err != nil {
		log.Fatalf("Unable to decode into struct: %v", err)
	}
	log.Infof("Config: %+v", config)
	return &config
}
