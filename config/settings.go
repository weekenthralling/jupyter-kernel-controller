package config

import (
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

type Config struct {
	ShellPort   int `mapstructure:"shell_port"`
	IOPubPort   int `mapstructure:"iopub_port"`
	StdinPort   int `mapstructure:"stdin_port"`
	HBPort      int `mapstructure:"hb_port"`
	ControlPort int `mapstructure:"control_port"`
}

func LoadConfig() *Config {

	// Enable Viper to read configuration from environment variables
	viper.AutomaticEnv()

	// Optional: Set a prefix for environment variables
	//viper.SetEnvPrefix("CONTROLLER")

	// Set default values
	viper.SetDefault("shell_port", "52700")
	viper.SetDefault("iopub_port", "52701")
	viper.SetDefault("hb_port", "52702")
	viper.SetDefault("control_port", "52703")
	viper.SetDefault("stdin_port", "52704")

	// Bind environment variables to Viper keys
	viper.BindEnv("shell_port")
	viper.BindEnv("iopub_port")
	viper.BindEnv("hb_port")
	viper.BindEnv("control_port")
	viper.BindEnv("stdin_port")

	var config Config
	// Read environment variables and decode into the Config struct
	if err := viper.Unmarshal(&config); err != nil {
		log.Fatalf("Unable to decode into struct: %v", err)
	}
	log.Infof("Config: %+v", config)
	return &config
}
