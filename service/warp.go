package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"s-ui/database/model"
	"s-ui/logger"
	"s-ui/util/common"
	"strconv"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

type WarpService struct{}

func (s *WarpService) getWarpInfo(deviceId string, accessToken string) ([]byte, error) {
	url := fmt.Sprintf("https://api.cloudflareclient.com/v0a2158/reg/%s", deviceId)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return nil, err
	}
	defer resp.Body.Close()
	buffer := bytes.NewBuffer(make([]byte, 8192))
	buffer.Reset()
	_, err = buffer.ReadFrom(resp.Body)
	if err != nil {
		return nil, err
	}

	return buffer.Bytes(), nil
}

func (s *WarpService) RegisterWarp(ep *model.Endpoint) error {
	tos := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	privateKey, _ := wgtypes.GenerateKey()
	publicKey := privateKey.PublicKey().String()
	hostName, _ := os.Hostname()

	data := fmt.Sprintf(`{"key":"%s","tos":"%s","type": "PC","model": "s-ui", "name": "%s"}`, publicKey, tos, hostName)
	url := "https://api.cloudflareclient.com/v0a2158/reg"

	req, err := http.NewRequest("POST", url, bytes.NewBuffer([]byte(data)))
	if err != nil {
		return err
	}

	req.Header.Add("CF-Client-Version", "a-7.21-0721")
	req.Header.Add("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return err
	}
	defer resp.Body.Close()
	buffer := bytes.NewBuffer(make([]byte, 8192))
	buffer.Reset()
	_, err = buffer.ReadFrom(resp.Body)
	if err != nil {
		return err
	}

	var rspData map[string]interface{}
	err = json.Unmarshal(buffer.Bytes(), &rspData)
	if err != nil {
		return err
	}

	deviceId, ok := rspData["id"].(string)
	if !ok {
		return fmt.Errorf("could not parse 'id' from registration response")
	}
	token, ok := rspData["token"].(string)
	if !ok {
		return fmt.Errorf("could not parse 'token' from registration response")
	}

	accountData, ok := rspData["account"].(map[string]interface{})
	if !ok {
		// It's possible account is not present, treat as non-fatal for now
		logger.Debug("No 'account' field in registration response.")
	}
	license := "" // Default to empty string if not found or error occurs
	if accountData != nil {
		licenseVal, licenseOk := accountData["license"].(string)
		if !licenseOk {
			logger.Debug("Could not parse 'license' from account data or license is not a string.")
			// Depending on requirements, this could be a fatal error.
			// For now, we proceed with an empty license.
		} else {
			license = licenseVal
		}
	}

	warpInfo, err := s.getWarpInfo(deviceId, token)
	if err != nil {
		return err
	}

	var warpDetails map[string]interface{}
	err = json.Unmarshal(warpInfo, &warpDetails)
	if err != nil {
		return err
	}

	warpConfig, ok := warpDetails["config"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("could not parse 'config' from warp details")
	}
	clientId, ok := warpConfig["client_id"].(string)
	if !ok {
		return fmt.Errorf("could not parse 'client_id' from warp config")
	}
	reserved := s.getReserved(clientId)
	interfaceConfig, ok := warpConfig["interface"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("could not parse 'interface' from warp config")
	}
	addresses, ok := interfaceConfig["addresses"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("could not parse 'addresses' from interface config")
	}
	v4, ok := addresses["v4"].(string)
	if !ok {
		return fmt.Errorf("could not parse 'v4' address from addresses")
	}
	v6, ok := addresses["v6"].(string)
	if !ok {
		return fmt.Errorf("could not parse 'v6' address from addresses")
	}
	peersList, ok := warpConfig["peers"].([]interface{})
	if !ok || len(peersList) == 0 {
		return fmt.Errorf("could not parse 'peers' array or it is empty")
	}
	peer, ok := peersList[0].(map[string]interface{})
	if !ok {
		return fmt.Errorf("could not parse first peer from peers list")
	}
	peerEndpointMap, ok := peer["endpoint"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("could not parse 'endpoint' from peer")
	}
	peerEndpoint, ok := peerEndpointMap["host"].(string)
	if !ok {
		return fmt.Errorf("could not parse 'host' from peer endpoint")
	}
	peerEpAddress, peerEpPort, err := net.SplitHostPort(peerEndpoint)
	if err != nil {
		return fmt.Errorf("could not split host and port from peer endpoint: %w", err)
	}
	peerPublicKey, ok := peer["public_key"].(string)
	if !ok {
		return fmt.Errorf("could not parse 'public_key' from peer")
	}
	peerPort, err := strconv.Atoi(peerEpPort)
	if err != nil {
		return fmt.Errorf("could not convert peer port to int: %w", err)
	}

	peers := []map[string]interface{}{
		{
			"address":     peerEpAddress,
			"port":        peerPort,
			"public_key":  peerPublicKey,
			"allowed_ips": []string{"0.0.0.0/0", "::/0"},
			"reserved":    reserved,
		},
	}

	warpData := map[string]interface{}{
		"access_token": token,
		"device_id":    deviceId,
		"license_key":  license,
	}

	ep.Ext, err = json.MarshalIndent(warpData, "", "  ")
	if err != nil {
		return err
	}

	var epOptions map[string]interface{}
	err = json.Unmarshal(ep.Options, &epOptions)
	if err != nil {
		return err
	}
	epOptions["private_key"] = privateKey.String()
	epOptions["address"] = []string{fmt.Sprintf("%s/32", v4), fmt.Sprintf("%s/128", v6)}
	epOptions["listen_port"] = 0
	epOptions["peers"] = peers

	ep.Options, err = json.MarshalIndent(epOptions, "", "  ")
	return err
}

func (s *WarpService) getReserved(clientID string) []int {
	var reserved []int
	decoded, err := base64.StdEncoding.DecodeString(clientID)
	if err != nil {
		return nil
	}

	hexString := ""
	for _, char := range decoded {
		hex := fmt.Sprintf("%02x", char)
		hexString += hex
	}

	for i := 0; i < len(hexString); i += 2 {
		hexByte := hexString[i : i+2]
		decValue, err := strconv.ParseInt(hexByte, 16, 32)
		if err != nil {
			return nil
		}
		reserved = append(reserved, int(decValue))
	}

	return reserved
}

func (s *WarpService) SetWarpLicense(old_license string, ep *model.Endpoint) error {
	var warpData map[string]string
	err := json.Unmarshal(ep.Ext, &warpData)
	if err != nil {
		return err
	}

	if warpData["license_key"] == old_license {
		return nil
	}

	url := fmt.Sprintf("https://api.cloudflareclient.com/v0a2158/reg/%s/account", warpData["device_id"])
	data := fmt.Sprintf(`{"license": "%s"}`, warpData["license_key"])

	req, err := http.NewRequest("PUT", url, bytes.NewBuffer([]byte(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+warpData["access_token"])

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	buffer := bytes.NewBuffer(make([]byte, 8192))
	buffer.Reset()
	_, err = buffer.ReadFrom(resp.Body)
	if err != nil {
		return err
	}
	var response map[string]interface{}
	err = json.Unmarshal(buffer.Bytes(), &response)
	if err != nil {
		return err
	}

	if success, ok := response["success"].(bool); ok && success == false {
		errorsVal, ok := response["errors"].([]interface{})
		if !ok || len(errorsVal) == 0 {
			return common.NewError("unknown_error", "warp license update failed with no error details")
		}
		errorObj, ok := errorsVal[0].(map[string]interface{})
		if !ok {
			return common.NewError("unknown_error", "warp license update failed with malformed error details")
		}
		codeVal, codeOk := errorObj["code"]
		msgVal, msgOk := errorObj["message"]
		if !codeOk || !msgOk {
			return common.NewError("unknown_error", "warp license update failed with incomplete error details")
		}
		codeStr, codeIsStr := codeVal.(string)
		msgStr, msgIsStr := msgVal.(string)
		if !codeIsStr || !msgIsStr { // Fallback if code or message are not strings
			return common.NewError("unknown_error", fmt.Sprintf("warp license update failed: %v", errorObj))
		}
		return common.NewError(codeStr, msgStr)
	}

	return nil
}
// trigger rebuild
