package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"text/template"

	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"

	"github.com/k0kubun/pp/v3"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	digest "github.com/JudgeGregg/go-http-digest-auth-client"
	openai "github.com/sashabaranov/go-openai"
)

type TemplateData struct {
	UserPrompt string
	Input      string
}

type device struct {
	RestParent     string `json:"restParent"`
	RestURL        string `json:"restURL"`
	NameURLEncoded string `json:"nameURLEncoded"`
	Name           string `json:"name"`
}

func main() {
	// Configure the console encoder with pretty log format
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "message",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalColorLevelEncoder,
		EncodeTime:     zapcore.TimeEncoderOfLayout("2006-01-02 15:04:05"),
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// Define command line flags
	ip := flag.String("ip", "10.10.0.140", "IP address of API")
	port := flag.String("port", "8176", "Port of API")
	list := flag.Bool("list", false, "List all devices")
	openAI := flag.Bool("AI", false, "AI")
	deviceName := flag.String("device", "", "Device to get brightness value for")
	queryText := flag.String("queryText", "Are the kitchen lights on?", "The question you want to ask the AI")
	alterDeviceStateFlag := flag.Bool("alterDeviceState", false, "Alter device state flag")
	deviceState := flag.String("deviceState", "", "State of the device: on, off, or dim")
	deviceStateParam := flag.String("deviceStateParam", "", "Parameter for device state: brightness level for 'dim'")
	flag.Parse()

	// Set up console encoder with pretty log format
	consoleEncoder := zapcore.NewConsoleEncoder(encoderConfig)

	// Create a Zap core using the console encoder
	core := zapcore.NewCore(consoleEncoder, zapcore.AddSync(os.Stdout), zap.InfoLevel)

	// Create the logger with a default level of Info
	logger := zap.New(core)

	// Set the verbosity flag based on your requirements
	verbosity := 1 // 0 for Error, 1 for Info, 2 for Debug, etc.

	// Adjust the logger's level based on the verbosity flag
	switch verbosity {
	case 0:
		logger = logger.WithOptions(zap.IncreaseLevel(zapcore.ErrorLevel))
	case 1:
		logger = logger.WithOptions(zap.IncreaseLevel(zapcore.InfoLevel))
	case 2:
		logger = logger.WithOptions(zap.IncreaseLevel(zapcore.DebugLevel))
	default:
		// Set a default level if necessary
		logger = logger.WithOptions(zap.IncreaseLevel(zapcore.InfoLevel))
	}

	// Use the logger to log different messages based on the verbosity level
	logger.Error("This is an error message")
	logger.Info("This is an info message")
	logger.Debug("This is a debug message")

	// Get username and password from environment variable
	auth := os.Getenv("INDIGO_AUTH")
	if auth == "" {
		fmt.Println("Error: INDIGO_AUTH environment variable not set")
		return
	}
	username := strings.Split(auth, ":")[0]
	password := strings.Split(auth, ":")[1]

	// Build URL for API request
	url := fmt.Sprintf("http://%s:%s/devices.json", *ip, *port)

	if *list {
		devices, err := getDeviceList(url, username, password)
		if err != nil {
			fmt.Println("Error:", err)
			return
		}
		printDeviceList(devices)
	} else if *deviceName != "" && !*alterDeviceStateFlag {
		deviceInfo, err := getDeviceInfo(*deviceName, *ip, *port, username, password)
		if err != nil {
			fmt.Println("Error:", err)
			return
		}
		printDeviceInfo(deviceInfo)
	} else if *openAI {
		devices, err := getDeviceList(url, username, password)
		if err != nil {
			fmt.Println("Error:", err)
			return
		}
		var deviceStrings []string
		for _, device := range devices {
			name := device.Name
			url := device.RestURL
			deviceStrings = append(deviceStrings, fmt.Sprintf("- %s (%s)", name, url))
		}

		deviceString := strings.Join(deviceStrings, ",")
		// Tell the AI to search for devices that match our request
		promptText, _ := generatePrompt(deviceString, *queryText, "prompt3.txt", logger)
		requestResponseFromAI, err := sendPromptString(promptText, logger)
		// Remove newlines for improved logging
		requestResponseFromAIstripped := strings.ReplaceAll(requestResponseFromAI, "\n", "")
		logger.Info(fmt.Sprintf("Response from LLM: %s", requestResponseFromAIstripped))
		// We give devices to the AI and it gives us an answer
		out, _ := parseAIrequest(requestResponseFromAI, *queryText, "prompt2.txt", ip, port, username, password, logger)
		// Remove newlines for improved logging
		outFromAIstripped := strings.ReplaceAll(strings.ReplaceAll(out, "\n", ""), " ", "")
		logger.Info(fmt.Sprintf("Response from LLM: %s", outFromAIstripped))
		err = CheckDesiredState(out, ip, port, username, password, logger)
		if err != nil {
			logger.Error(fmt.Sprintf("changeState returned an error: %s", err))
			return
		}

		return

	} else if *alterDeviceStateFlag && *deviceName != "" && *deviceState != "" {
		deviceInfo, err := alterDeviceState(*deviceName, *deviceState, *deviceStateParam, *ip, *port, username, password)
		if err != nil {
			fmt.Println("alterdeviceState error:", err)
			return
		}
		fmt.Println("Device state altered successfully. New state:")
		printDeviceInfo(deviceInfo)
	} else {
		fmt.Println("No command specified. Use -list to list devices or -device <device name> to get device information.")
	}
}

func getDeviceList(url, username, password string) ([]device, error) {
	// Make request to API to get list of devices
	t := digest.NewTransport(username, password)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := t.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Parse response as JSON
	var devices []device
	err = json.NewDecoder(resp.Body).Decode(&devices)

	if err != nil {
		return nil, err
	}

	return devices, nil
}

func printDeviceList(devices []device) {
	// Print list of device names and URLs
	fmt.Println("Devices:")
	for _, device := range devices {
		name := device.Name
		url := device.RestURL
		fmt.Printf("- %s (%s)\n", name, url)
	}
}

//curl -X PUT -d isOn=0 http://127.0.0.1:8176/devices/office-lamp
//curl -X PUT -d isOn=1 http://127.0.0.1:8176/devices/office-lamp

func alterDeviceState(deviceName, deviceState, deviceStateParam, ip, port, username, password string) (map[string]interface{}, error) {
	// Build URL for API request for specific device
	//deviceURL := fmt.Sprintf("/devices/%s", url.PathEscape(deviceName)) // encode the device name
	//pp.Print(deviceURL)
	var urlString string
	if deviceState == "off" {
		urlString = fmt.Sprintf("http://%s:%s%s?isOn=0", ip, port, deviceName)
	} else if deviceState == "on" {
		urlString = fmt.Sprintf("http://%s:%s%s?isOn=1", ip, port, deviceName)
	} else if deviceState == "dim" {
		urlString = fmt.Sprintf("http://%s:%s%s?brightness=%s", ip, port, deviceName, deviceStateParam)
	}

	// Make authenticated request to API to alter device state
	t := digest.NewTransport(username, password)
	req, _ := http.NewRequest("PUT", urlString, nil) // changing the method to GET
	//pp.Print(req)

	resp, _ := t.RoundTrip(req)
	//pp.Print(resp)
	pp.Print(resp.StatusCode)

	if resp != nil && resp.StatusCode == http.StatusUnauthorized {
		return map[string]interface{}{"status": "ok"}, nil
	}
	defer resp.Body.Close()

	// Parse response as JSON
	var device map[string]interface{}
	err := json.NewDecoder(resp.Body).Decode(&device)
	if err != nil {
		return nil, err
	}

	return device, nil
}

func getDeviceInfo(deviceName, ip, port, username, password string) (map[string]interface{}, error) {
	// Build URL for API request for specific device
	//deviceURL := fmt.Sprintf("/devices/%s.json", strings.ReplaceAll(deviceName, " ", "%20"))
	url := fmt.Sprintf("http://%s:%s%s", ip, port, deviceName)

	// Make request to API to get device information
	t := digest.NewTransport(username, password)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := t.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Parse response as JSON
	var device map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&device)
	if err != nil {
		return nil, err
	}

	return device, nil
}

func getDeviceInfoPath(deviceURL, ip, port, username, password string) (map[string]interface{}, error) {
	// Build URL for API request for specific device
	url := fmt.Sprintf("http://%s:%s%s", ip, port, deviceURL)

	// Make request to API to get device information
	t := digest.NewTransport(username, password)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := t.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Parse response as JSON
	var device map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&device)
	if err != nil {
		return nil, err
	}

	return device, nil
}

func printDeviceInfo(device map[string]interface{}) {
	// Print all keys and values in map
	fmt.Println("Device information:")
	for key, value := range device {
		fmt.Printf("%s: %v\n", key, value)
	}
}

func parseAIrequest(request string, prompt string, filename string, ip *string, port *string, username string, password string, logger *zap.Logger) (string, error) {
	// Parse JSON returned by AI and make requests
	var data map[string]interface{}
	err := json.Unmarshal([]byte(request), &data)
	if err != nil {

		request += "}"
		decoder := json.NewDecoder(strings.NewReader(request))
		err = decoder.Decode(&data)

		if err != nil {
			return "Error", err
		}
	}

	// Extract device paths
	devicePaths := data["devicePaths"].([]interface{})

	// Initialize the big string
	deviceString := ""
	// Loop over each device path
	for _, path := range devicePaths {
		devicePath := path.(string)
		deviceInfo, err := getDeviceInfoPath(devicePath, *ip, *port, username, password)
		if err != nil {
			fmt.Println("Error in praseAIrequest: Couldn't parse device. ", err)
			continue
		}
		//printDeviceInfo(deviceInfo)

		// Append device information to the big string
		deviceString += fmt.Sprintf("%s %+v", devicePath, deviceInfo) + "\n"
		deviceString += "\n"
	}

	promptText, _ := generatePrompt(deviceString, prompt, filename, logger)
	output, err := sendPromptString(promptText, logger)
	if err != nil {
		return "Error", nil
	} else {
		return output, nil
	}
}

func CheckDesiredState(jsonData string, ip *string, port *string, username string, password string, logger *zap.Logger) error {
	// Parse JSON
	var data map[string]interface{}
	logger.Info(fmt.Sprintf("Interpreting the desired state"))
	decoder := json.NewDecoder(strings.NewReader(jsonData))
	err := decoder.Decode(&data)
	if err != nil {
		if err == io.ErrUnexpectedEOF {
			jsonData += "}"
			decoder := json.NewDecoder(strings.NewReader(jsonData))
			err = decoder.Decode(&data)
		}
		if err != nil {
			return err
		}
	} //if err != nil {
	//	return "Error", err
	//}
	logger.Info(fmt.Sprintf("Response from LLM: %s", data["text"]))

	// Extract "devices" array

	devices, ok := data["devices"].([]interface{})
	if !ok {
		logger.Error(fmt.Sprintf("'devices' key not found or has an invalid type"))
		return errors.New("'devices' key not found or has an invalid type")
	}

	// Iterate through devices
	for _, device := range devices {
		deviceMap, ok := device.(map[string]interface{})
		if !ok {
			// Invalid device format
			pp.Print("invalid device format?")
			continue
		}

		desiredState, ok := deviceMap["desiredState"].(string)
		if ok {
			// "desiredState" key exists in the current device
			if desiredState == "on" {
				logger.Info(fmt.Sprintf("The device %s is turning on", deviceMap["devicePath"]))
				alterDeviceState(deviceMap["devicePath"].(string), "on", "", *ip, *port, username, password)
			} else if desiredState == "off" {
				logger.Info(fmt.Sprintf("The device %s is turning off", deviceMap["devicePath"]))
				alterDeviceState(deviceMap["devicePath"].(string), "off", "", *ip, *port, username, password)
			}
		}
	}

	return nil
}

func generatePrompt(input string, prompt string, filename string, logger *zap.Logger) (string, error) {
	// Read the query from the file
	queryBytes, err := ioutil.ReadFile(filename)
	if err != nil {
		logger.Error("Error reading file", zap.Error(err))
		return "", err
	}
	query := string(queryBytes)

	data := TemplateData{
		UserPrompt: prompt,
		Input:      input,
	}

	tmpl, err := template.New("queryTemplate").Parse(query)
	if err != nil {
		logger.Error("Error parsing template", zap.Error(err))
		return "", err
	}

	var outputBuilder strings.Builder
	err = tmpl.Execute(&outputBuilder, data)
	if err != nil {
		logger.Error("Error executing template", zap.Error(err))
		return "", err
	}
	output := outputBuilder.String()

	return output, nil

}

func sendPromptString(prompt string, logger *zap.Logger) (string, error) {
	// Read file contents into a string

	truncatedPrompt := strings.Join(strings.Fields(prompt)[len(strings.Fields(prompt))-10:], " ")
	if logger.Core().Enabled(zapcore.DebugLevel) {
		logger.Debug(fmt.Sprintf("Prompt sent to LLM: ``` %s ```", prompt))
	} else {
		logger.Info(fmt.Sprintf("Truncated prompt sent to LLM: ``` %s ```", truncatedPrompt))
	}

	config := openai.DefaultAzureConfig("dummy", "http://10.10.0.129:5001", "gpt-3.5-turbo")
	client := openai.NewClientWithConfig(config)
	//client := openai.NewClient("")
	resp, err := client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			MaxTokens:   1643,
			Model:       openai.GPT3Dot5Turbo,
			Temperature: 0.9,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleUser,
					Content: prompt,
				},
			},
		},
	)

	if err != nil {
		return "", fmt.Errorf("ChatCompletion error: %v", err)
	}

	return resp.Choices[0].Message.Content, nil
}
