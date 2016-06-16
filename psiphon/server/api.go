/*
 * Copyright (c) 2016, Psiphon Inc.
 * All rights reserved.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/Psiphon-Labs/psiphon-tunnel-core/psiphon"
)

const MAX_API_PARAMS_SIZE = 256 * 1024 // 256KB

type requestJSONObject map[string]interface{}

// sshAPIRequestHandler routes Psiphon API requests transported as
// JSON objects via the SSH request mechanism.
//
// The API request handlers, handshakeAPIRequestHandler, etc., are
// reused by webServer which offers the Psiphon API via web transport.
//
// The API request parameters and event log values follow the legacy
// psi_web protocol and naming conventions. The API is compatible all
// tunnel-core clients but are not backwards compatible with older
// clients.
//
func sshAPIRequestHandler(
	config *Config, geoIPData GeoIPData, name string, requestPayload []byte) ([]byte, error) {

	// Note: for SSH requests, MAX_API_PARAMS_SIZE is implicitly enforced
	// by max SSH reqest packet size.

	var params requestJSONObject
	err := json.Unmarshal(requestPayload, &params)
	if err != nil {
		return nil, psiphon.ContextError(err)
	}

	switch name {
	case psiphon.SERVER_API_HANDSHAKE_REQUEST_NAME:
		return handshakeAPIRequestHandler(config, geoIPData, params)
	case psiphon.SERVER_API_CONNECTED_REQUEST_NAME:
		return connectedAPIRequestHandler(config, geoIPData, params)
	case psiphon.SERVER_API_STATUS_REQUEST_NAME:
		return statusAPIRequestHandler(config, geoIPData, params)
	case psiphon.SERVER_API_CLIENT_VERIFICATION_REQUEST_NAME:
		return clientVerificationAPIRequestHandler(config, geoIPData, params)
	}

	return nil, psiphon.ContextError(fmt.Errorf("invalid request name: %s", name))
}

// handshakeAPIRequestHandler implements the "handshake" API request.
// Clients make the handshake immediately after establishing a tunnel
// connection; the response tells the client what homepage to open, what
// stats to record, etc.
func handshakeAPIRequestHandler(
	config *Config, geoIPData GeoIPData, params requestJSONObject) ([]byte, error) {

	// Note: ignoring "known_servers" params

	err := validateRequestParams(config, params, baseRequestParams)
	if err != nil {
		// TODO: fail2ban?
		return nil, psiphon.ContextError(errors.New("invalid params"))
	}

	log.WithContextFields(
		getRequestLogFields(
			config,
			"handshake",
			geoIPData,
			params,
			baseRequestParams)).Info("API event")

	// TODO: share struct definition with psiphon/serverApi.go?
	// TODO: populate response data using psinet database

	var handshakeResponse struct {
		Homepages            []string            `json:"homepages"`
		UpgradeClientVersion string              `json:"upgrade_client_version"`
		PageViewRegexes      []map[string]string `json:"page_view_regexes"`
		HttpsRequestRegexes  []map[string]string `json:"https_request_regexes"`
		EncodedServerList    []string            `json:"encoded_server_list"`
		ClientRegion         string              `json:"client_region"`
		ServerTimestamp      string              `json:"server_timestamp"`
	}

	handshakeResponse.ServerTimestamp = psiphon.GetCurrentTimestamp()

	responsePayload, err := json.Marshal(handshakeResponse)
	if err != nil {
		return nil, psiphon.ContextError(err)
	}

	return responsePayload, nil
}

var connectedRequestParams = append(
	[]requestParamSpec{requestParamSpec{"last_connected", isLastConnected, 0}},
	baseRequestParams...)

// connectedAPIRequestHandler implements the "connected" API request.
// Clients make the connected request once a tunnel connection has been
// established and at least once per day. The last_connected input value,
// which should be a connected_timestamp output from a previous connected
// response, is used to calculate unique user stats.
func connectedAPIRequestHandler(
	config *Config, geoIPData GeoIPData, params requestJSONObject) ([]byte, error) {

	err := validateRequestParams(config, params, connectedRequestParams)
	if err != nil {
		// TODO: fail2ban?
		return nil, psiphon.ContextError(errors.New("invalid params"))
	}

	log.WithContextFields(
		getRequestLogFields(
			config,
			"connected",
			geoIPData,
			params,
			connectedRequestParams)).Info("API event")

	var connectedResponse struct {
		ConnectedTimestamp string `json:"connected_timestamp"`
	}

	connectedResponse.ConnectedTimestamp =
		psiphon.TruncateTimestampToHour(psiphon.GetCurrentTimestamp())

	responsePayload, err := json.Marshal(connectedResponse)
	if err != nil {
		return nil, psiphon.ContextError(err)
	}

	return responsePayload, nil
}

var statusRequestParams = append(
	[]requestParamSpec{requestParamSpec{"connected", isBooleanFlag, 0}},
	baseRequestParams...)

// statusAPIRequestHandler implements the "status" API request.
// Clients make periodic status requests which deliver client-side
// recorded data transfer and tunnel duration stats.
func statusAPIRequestHandler(
	config *Config, geoIPData GeoIPData, params requestJSONObject) ([]byte, error) {

	err := validateRequestParams(config, params, statusRequestParams)
	if err != nil {
		// TODO: fail2ban?
		return nil, psiphon.ContextError(errors.New("invalid params"))
	}

	statusData, err := getJSONObjectRequestParam(params, "statusData")
	if err != nil {
		return nil, psiphon.ContextError(err)
	}

	// Overall bytes transferred stats

	bytesTransferred, err := getInt64RequestParam(statusData, "bytes_transferred")
	if err != nil {
		return nil, psiphon.ContextError(err)
	}
	bytesTransferredFields := getRequestLogFields(
		config, "bytes_transferred", geoIPData, params, statusRequestParams)
	bytesTransferredFields["bytes"] = bytesTransferred
	log.WithContextFields(bytesTransferredFields).Info("API event")

	// Domain bytes transferred stats

	hostBytes, err := getMapStringInt64RequestParam(statusData, "host_bytes")
	if err != nil {
		return nil, psiphon.ContextError(err)
	}
	domainBytesFields := getRequestLogFields(
		config, "domain_bytes", geoIPData, params, statusRequestParams)
	for domain, bytes := range hostBytes {
		domainBytesFields["domain"] = domain
		domainBytesFields["bytes"] = bytes
		log.WithContextFields(domainBytesFields).Info("API event")
	}

	// Tunnel duration and bytes transferred stats

	tunnelStats, err := getJSONObjectArrayRequestParam(statusData, "tunnel_stats")
	if err != nil {
		return nil, psiphon.ContextError(err)
	}
	sessionFields := getRequestLogFields(
		config, "session", geoIPData, params, statusRequestParams)
	for _, tunnelStat := range tunnelStats {

		sessionID, err := getStringRequestParam(tunnelStat, "session_id")
		if err != nil {
			return nil, psiphon.ContextError(err)
		}
		sessionFields["session_id"] = sessionID

		tunnelNumber, err := getInt64RequestParam(tunnelStat, "tunnel_number")
		if err != nil {
			return nil, psiphon.ContextError(err)
		}
		sessionFields["tunnel_number"] = tunnelNumber

		tunnelServerIPAddress, err := getStringRequestParam(tunnelStat, "tunnel_server_ip_address")
		if err != nil {
			return nil, psiphon.ContextError(err)
		}
		sessionFields["tunnel_server_ip_address"] = tunnelServerIPAddress

		serverHandshakeTimestamp, err := getStringRequestParam(tunnelStat, "server_handshake_timestamp")
		if err != nil {
			return nil, psiphon.ContextError(err)
		}
		sessionFields["server_handshake_timestamp"] = serverHandshakeTimestamp

		duration, err := getInt64RequestParam(tunnelStat, "duration")
		if err != nil {
			return nil, psiphon.ContextError(err)
		}
		// Client reports durations in nanoseconds; divide to get to milliseconds
		sessionFields["duration"] = duration / 1000000

		totalBytesSent, err := getInt64RequestParam(tunnelStat, "total_bytes_sent")
		if err != nil {
			return nil, psiphon.ContextError(err)
		}
		sessionFields["total_bytes_sent"] = totalBytesSent

		totalBytesReceived, err := getInt64RequestParam(tunnelStat, "total_bytes_received")
		if err != nil {
			return nil, psiphon.ContextError(err)
		}
		sessionFields["total_bytes_received"] = totalBytesReceived

		log.WithContextFields(sessionFields).Info("API event")
	}

	return make([]byte, 0), nil
}

// clientVerificationAPIRequestHandler implements the
// "client verification" API request. Clients make the client
// verification request once per tunnel connection. The payload
// attests that client is a legitimate Psiphon client.
func clientVerificationAPIRequestHandler(
	config *Config, geoIPData GeoIPData, params requestJSONObject) ([]byte, error) {

	err := validateRequestParams(config, params, baseRequestParams)
	if err != nil {
		// TODO: fail2ban?
		return nil, psiphon.ContextError(errors.New("invalid params"))
	}

	// TODO: implement

	return make([]byte, 0), nil
}

type requestParamSpec struct {
	name      string
	validator func(*Config, string) bool
	flags     int32
}

const (
	requestParamOptional  = 1
	requestParamNotLogged = 2
)

// baseRequestParams is the list of required and optional
// request parameters; derived from COMMON_INPUTS and
// OPTIONAL_COMMON_INPUTS in psi_web.
var baseRequestParams = []requestParamSpec{
	requestParamSpec{"server_secret", isServerSecret, requestParamNotLogged},
	requestParamSpec{"client_session_id", isHexDigits, 0},
	requestParamSpec{"propagation_channel_id", isHexDigits, 0},
	requestParamSpec{"sponsor_id", isHexDigits, 0},
	requestParamSpec{"client_version", isDigits, 0},
	requestParamSpec{"client_platform", isClientPlatform, 0},
	requestParamSpec{"relay_protocol", isRelayProtocol, 0},
	requestParamSpec{"tunnel_whole_device", isBooleanFlag, 0},
	requestParamSpec{"device_region", isRegionCode, requestParamOptional},
	requestParamSpec{"meek_dial_address", isDialAddress, requestParamOptional},
	requestParamSpec{"meek_resolved_ip_address", isIPAddress, requestParamOptional},
	requestParamSpec{"meek_sni_server_name", isDomain, requestParamOptional},
	requestParamSpec{"meek_host_header", isHostHeader, requestParamOptional},
	requestParamSpec{"meek_transformed_host_name", isBooleanFlag, requestParamOptional},
	requestParamSpec{"server_entry_region", isRegionCode, requestParamOptional},
	requestParamSpec{"server_entry_source", isServerEntrySource, requestParamOptional},
	requestParamSpec{"server_entry_timestamp", isISO8601Date, requestParamOptional},
}

func validateRequestParams(
	config *Config,
	params requestJSONObject,
	expectedParams []requestParamSpec) error {

	for _, expectedParam := range expectedParams {
		value := params[expectedParam.name]
		if value == nil {
			if expectedParam.flags&requestParamOptional != 0 {
				continue
			}
			return psiphon.ContextError(
				fmt.Errorf("missing required param: %s", expectedParam.name))
		}
		strValue, ok := value.(string)
		if !ok {
			return psiphon.ContextError(
				fmt.Errorf("unexpected param type: %s", expectedParam.name))
		}
		if !expectedParam.validator(config, strValue) {
			return psiphon.ContextError(
				fmt.Errorf("invalid param: %s", expectedParam.name))
		}
	}

	return nil
}

// getRequestLogFields makes LogFields to log the API event following
// the legacy psi_web and current ELK naming conventions.
func getRequestLogFields(
	config *Config,
	eventName string,
	geoIPData GeoIPData,
	params requestJSONObject,
	expectedParams []requestParamSpec) LogFields {

	logFields := make(LogFields)

	logFields["event_name"] = eventName
	logFields["host_id"] = config.HostID

	// In psi_web, the space replacement was done to accommodate space
	// delimited logging, which is no longer required; we retain the
	// transformation so that stats aggregation isn't impacted.
	logFields["client_region"] = strings.Replace(geoIPData.Country, " ", "_", -1)
	logFields["client_city"] = strings.Replace(geoIPData.City, " ", "_", -1)
	logFields["client_isp"] = strings.Replace(geoIPData.ISP, " ", "_", -1)

	for _, expectedParam := range expectedParams {
		value := params[expectedParam.name]
		if value == nil {
			// Skip optional params
			continue
		}
		strValue, ok := value.(string)
		if !ok {
			// This type assertion should be checked already in
			// validateRequestParams, so failure is unexpected.
			continue
		}
		// Special cases:
		// - Number fields are encoded as integer types.
		// - For ELK performance we record these domain-or-IP
		//   fields as one of two different values based on type;
		//   we also omit port from host:port fields for now.
		switch expectedParam.name {
		case "client_version":
			intValue, _ := strconv.Atoi(strValue)
			logFields[expectedParam.name] = intValue
		case "meek_dial_address":
			host, _, _ := net.SplitHostPort(strValue)
			if isIPAddress(config, host) {
				logFields["meek_dial_ip_address"] = host
			} else {
				logFields["meek_dial_domain"] = host
			}
		case "meek_host_header":
			host, _, _ := net.SplitHostPort(strValue)
			logFields[expectedParam.name] = host
		default:
			logFields[expectedParam.name] = strValue
		}
	}

	return logFields
}

func getStringRequestParam(params requestJSONObject, name string) (string, error) {
	if params[name] == nil {
		return "", psiphon.ContextError(errors.New("missing param"))
	}
	value, ok := params[name].(string)
	if !ok {
		return "", psiphon.ContextError(errors.New("invalid param"))
	}
	return value, nil
}

func getInt64RequestParam(params requestJSONObject, name string) (int64, error) {
	if params[name] == nil {
		return 0, psiphon.ContextError(errors.New("missing param"))
	}
	value, ok := params[name].(int64)
	if !ok {
		return 0, psiphon.ContextError(errors.New("invalid param"))
	}
	return value, nil
}

func getJSONObjectRequestParam(params requestJSONObject, name string) (requestJSONObject, error) {
	if params[name] == nil {
		return nil, psiphon.ContextError(errors.New("missing param"))
	}
	value, ok := params[name].(requestJSONObject)
	if !ok {
		return nil, psiphon.ContextError(errors.New("invalid param"))
	}
	return value, nil
}

func getJSONObjectArrayRequestParam(params requestJSONObject, name string) ([]requestJSONObject, error) {
	if params[name] == nil {
		return nil, psiphon.ContextError(errors.New("missing param"))
	}
	value, ok := params[name].([]requestJSONObject)
	if !ok {
		return nil, psiphon.ContextError(errors.New("invalid param"))
	}
	return value, nil
}

func getMapStringInt64RequestParam(params requestJSONObject, name string) (map[string]int64, error) {
	if params[name] == nil {
		return nil, psiphon.ContextError(errors.New("missing param"))
	}
	value, ok := params[name].(map[string]int64)
	if !ok {
		return nil, psiphon.ContextError(errors.New("invalid param"))
	}
	return value, nil
}

// Input validators follow the legacy validations rules in psi_web.

func isServerSecret(config *Config, value string) bool {
	return subtle.ConstantTimeCompare(
		[]byte(value),
		[]byte(config.WebServerSecret)) == 1
}

func isHexDigits(_ *Config, value string) bool {
	return -1 == strings.IndexFunc(value, func(c rune) bool {
		return !unicode.Is(unicode.ASCII_Hex_Digit, c)
	})
}

func isDigits(_ *Config, value string) bool {
	return -1 == strings.IndexFunc(value, func(c rune) bool {
		return c < '0' || c > '9'
	})
}

func isClientPlatform(_ *Config, value string) bool {
	return -1 == strings.IndexFunc(value, func(c rune) bool {
		// Note: stricter than psi_web's Python string.whitespace
		return unicode.Is(unicode.White_Space, c)
	})
}

func isRelayProtocol(_ *Config, value string) bool {
	return psiphon.Contains(psiphon.SupportedTunnelProtocols, value)
}

func isBooleanFlag(_ *Config, value string) bool {
	return value == "0" || value == "1"
}

func isRegionCode(_ *Config, value string) bool {
	if len(value) != 2 {
		return false
	}
	return -1 == strings.IndexFunc(value, func(c rune) bool {
		return c < 'A' || c > 'Z'
	})
}

func isDialAddress(config *Config, value string) bool {
	// "<host>:<port>", where <host> is a domain or IP address
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return false
	}
	if !isIPAddress(config, parts[0]) && !isDomain(config, parts[0]) {
		return false
	}
	if !isDigits(config, parts[1]) {
		return false
	}
	port, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	return port > 0 && port < 65536
}

func isIPAddress(_ *Config, value string) bool {
	return net.ParseIP(value) != nil
}

var isDomainRegex = regexp.MustCompile("[a-zA-Z\\d-]{1,63}$")

func isDomain(_ *Config, value string) bool {

	// From: http://stackoverflow.com/questions/2532053/validate-a-hostname-string
	//
	// "ensures that each segment
	//    * contains at least one character and a maximum of 63 characters
	//    * consists only of allowed characters
	//    * doesn't begin or end with a hyphen"
	//

	if len(value) > 255 {
		return false
	}
	value = strings.TrimSuffix(value, ".")
	for _, part := range strings.Split(value, ".") {
		// Note: regexp doesn't support the following Perl expression which
		// would check for '-' prefix/suffix: "(?!-)[a-zA-Z\\d-]{1,63}(?<!-)$"
		if strings.HasPrefix(part, "-") || strings.HasSuffix(part, "-") {
			return false
		}
		if !isDomainRegex.Match([]byte(part)) {
			return false
		}
	}
	return true
}

func isHostHeader(config *Config, value string) bool {
	// "<host>:<port>", where <host> is a domain or IP address and ":<port>" is optional
	if strings.Contains(value, ":") {
		return isDialAddress(config, value)
	}
	return isIPAddress(config, value) || isDomain(config, value)
}

func isServerEntrySource(_ *Config, value string) bool {
	return psiphon.Contains(psiphon.SupportedServerEntrySources, value)
}

var isISO8601DateRegex = regexp.MustCompile(
	"(?P<year>[0-9]{4})-(?P<month>[0-9]{1,2})-(?P<day>[0-9]{1,2})T(?P<hour>[0-9]{2}):(?P<minute>[0-9]{2}):(?P<second>[0-9]{2})(\\.(?P<fraction>[0-9]+))?(?P<timezone>Z|(([-+])([0-9]{2}):([0-9]{2})))")

func isISO8601Date(_ *Config, value string) bool {
	return isISO8601DateRegex.Match([]byte(value))
}

func isLastConnected(config *Config, value string) bool {
	return value == "None" || value == "Unknown" || isISO8601Date(config, value)
}
