package output

import (
	"crypto/tls"
	"errors"
	conf "github.com/carbonblack/cb-event-forwarder/internal/config"
	"github.com/carbonblack/cb-event-forwarder/internal/encoder"
	log "github.com/sirupsen/logrus"
	"golang.org/x/oauth2/jwt"
	"os"
	"path"
	"plugin"
	"strconv"
	"sync"
	"time"
)

//Output handler interface

type OutputHandler interface {
	//messages are the in-flight messages channel - errorchan is a channel overwhich to report errors, controlchan sends signals
	// the Outputhandler is required to honor the signals sent down by it's controller
	// The waitgroup will be marked done when the output has finished
	Go(messages <-chan map[string]interface{}, errorChan chan<- error, controlchan <-chan os.Signal, wg sync.WaitGroup) error
	String() string
	Statistics() interface{}
	Key() string
}

// Trty to load an pluginName.so at pluginPath w/ cfg configuration and e , the Encoder to use to format output.
func loadOutputFromPlugin(pluginPath string, pluginName string, cfg map[interface{}]interface{}, e encoder.Encoder) (OutputHandler, error) {
	log.Infof("loadOutputFromPlugin: Trying to load plugin %s at %s", pluginName, pluginPath)
	plug, err := plugin.Open(path.Join(pluginPath, pluginName+".so"))
	if err != nil {
		log.Panic(err)
	}
	pluginHandlerFuncRaw, err := plug.Lookup("GetOutputHandler")
	if err != nil {
		log.Panicf("Failed to load plugin %v", err)
	}
	return pluginHandlerFuncRaw.(func(map[interface{}]interface{}, encoder.Encoder) (OutputHandler, error))(cfg, e)
}

/*
Process the output:
                  -type-of-input:
in the configuration file into an array of outputs, or output a relevant error that is encountered

*/
func GetOutputFromCfg(outputCfg map[interface{}] interface{}) (OutputHandler, error) {
	var tempOH OutputHandler
	var tlsConfig *tls.Config = nil
	var jwtConfig *jwt.Config = nil
	var err error = nil
	var myencoder encoder.Encoder
	var outputMap map [interface{}] interface{}

	var outputtype string = "NOOUTPUTFOUND"
	for k := range outputCfg {
		outputtype = k.(string)
		outputMap = outputCfg[k].(map[interface{}] interface{})
        	break
    	}

	log.Infof("trying to process output with cfg %s", outputCfg)

	if t, ok := outputMap["format"]; ok {
		myencoder, err = encoder.GetEncoderFromCfg(t.(map[interface{}]interface{}))
	} else {
		return tempOH, err
	}

	switch outputtype {
	case "file":
		path, ok := outputMap["path"]
		if !ok {
			return tempOH, errors.New("Couldn't find path in file output section")
		}
		var rolloverSizeBytes int64 = 500000
		var rolloverDuration time.Duration
		rollOverDuration, ok := outputMap["rollover_duration"]
		if ok {
			rolloverDuration = time.Duration(rollOverDuration.(int)) * time.Second

		} else {
			rolloverDuration = time.Duration(86400) * time.Second
		}
		rollOverSizeBytes, ok := outputMap["rollover_size_bytes"]
		if ok {
			s, err := strconv.ParseInt(rollOverSizeBytes.(string), 10, 64)
			if err != nil {
				log.Panicf("%v", err)
			}
			rolloverSizeBytes = s
		}
		fo := NewFileOutputHandler(path.(string), rolloverSizeBytes, rolloverDuration, myencoder)
		tempOH = &fo
	case "syslog":
		connectionString, ok := outputMap["connection"]
		if !ok {
		}
		if tlsCfg, ok := outputMap["tls"]; ok {
			tlsConfig, _ = conf.GetTLSConfigFromCfg(tlsCfg.(map[interface{}]interface{}))
		} else {
			return tempOH, errors.New("Couldn't find connection in syslog output section")
		}
		so, err := NewSyslogOutput(connectionString.(string), tlsConfig, myencoder)
		if err == nil {
			tempOH = &so
		} else {
			return tempOH, err
		}

	case "socket":
		connectionString, ok := outputMap["connection"]
		if !ok {
			return tempOH, errors.New("Coudn't find connection in socket output section")
		}
		no, err := NewNetOutputHandler(connectionString.(string), myencoder)
		if err != nil {
			return tempOH, err
		}
		tempOH = &no
	case "http":
		if tlsCfg, ok := outputMap["tls"]; ok {
			tlsConfig, _ = conf.GetTLSConfigFromCfg(tlsCfg.(map[interface{}]interface{}))
		}
		if jwtCfg, ok := outputMap["jwt"]; ok {
			jwtConfig, _ = conf.GetJWTConfigFromCfg(jwtCfg.(map[interface{}]interface{}))
		}
		//bundle_size_max,bundle_send_timeout, upload_empty_files
		var bundle_size_max, bundle_send_timeout int64
		var upload_empty_files bool
		if b, ok := outputMap["upload_empty_files"]; ok {
			upload_empty_files = b.(bool)
		}
		if t, ok := outputMap["bundle_size_max"]; ok {
			s, err := strconv.ParseInt(t.(string), 10, 64)
			if err != nil {
				log.Panicf("%v", err)
			}
			bundle_size_max = s
		}
		if t, ok := outputMap["bundle_send_timeout"]; ok {
			s, err := strconv.ParseInt(t.(string), 10, 64)
			if err != nil {
				log.Panicf("%v", err)
			}
			bundle_send_timeout = s
		}
		bundleLocation := "/var/cb/data/event-forwarder"
		if t, ok := outputMap["bundle_directory"]; ok {
			bundleLocation, _ = t.(string)
		}
		httpBundleBehavior, err := HTTPBehaviorFromCfg(outputMap, true, "/tmp", jwtConfig, tlsConfig)
		if err == nil {
			bo, err := NewBundledOutput(bundleLocation, bundle_size_max, bundle_send_timeout, upload_empty_files, true, "/tmp", httpBundleBehavior, myencoder)
			if err != nil {
				log.Infof("Error making BO for HTTP %s", err)
				return tempOH, err
			}
			tempOH = &bo
		} else {
			log.Panicf("Couldn't create bundled output for HTTP out... %s", err)
		}
	case "splunk":
		if tlsCfg, ok := outputMap["tls"]; ok {
			tlsConfig, _ = conf.GetTLSConfigFromCfg(tlsCfg.(map[interface{}]interface{}))
		}
		//bundle_size_max,bundle_send_timeout, upload_empty_files
		var bundle_size_max, bundle_send_timeout int64
		var upload_empty_files bool
		if b, ok := outputMap["upload_empty_files"]; ok {
			upload_empty_files = b.(bool)
		}
		if t, ok := outputMap["bundle_size_max"]; ok {
			s, err := strconv.ParseInt(t.(string), 10, 64)
			if err != nil {
				log.Panicf("%v", err)
			}
			bundle_size_max = s
		}
		if t, ok := outputMap["bundle_send_timeout"]; ok {
			s, err := strconv.ParseInt(t.(string), 10, 64)
			if err != nil {
				log.Panicf("%v", err)
			}
			bundle_send_timeout = s
		}
		bundleLocation := "/var/cb/data/event-forwarder"
		if t, ok := outputMap["bundle_directory"]; ok {
			bundleLocation, _ = t.(string)
		}
		splunkBundleBehavior, err := SplunkBehaviorFromCfg(outputMap, true, "/tmp", tlsConfig)
		if err == nil {
			bo, err := NewBundledOutput(bundleLocation, bundle_size_max, bundle_send_timeout, upload_empty_files, true, "/tmp", splunkBundleBehavior, myencoder)
			if err != nil {
				return tempOH, err
			}
			tempOH = &bo
		} else {
			log.Panicf("Couldn't create bundled output for Splunk %s", err)
		}
	case "s3":
		var bundle_size_max, bundle_send_timeout int64
		var upload_empty_files bool
		if b, ok := outputMap["upload_empty_files"]; ok {
			upload_empty_files = b.(bool)
		}
		if t, ok := outputMap["bundle_size_max"]; ok {
			s, err := strconv.ParseInt(t.(string), 10, 64)
			if err != nil {
				log.Panicf("%v", err)
			}
			bundle_size_max = s
		}
		if t, ok := outputMap["bundle_send_timeout"]; ok {
			s, err := strconv.ParseInt(t.(string), 10, 64)
			if err != nil {
				log.Panicf("%v", err)
			}
			bundle_send_timeout = s
		}
		s3BundleBehavior, err := S3BehaviorFromCfg(outputMap)
		bundleLocation := "/var/cb/data/event-forwarder"
		if t, ok := outputMap["bundle_directory"]; ok {
			bundleLocation, _ = t.(string)
		}
		if err == nil {
			bo, err := NewBundledOutput(bundleLocation, bundle_size_max, bundle_send_timeout, upload_empty_files, true, "/tmp", &s3BundleBehavior, myencoder)
			if err != nil {
				return tempOH, err
			}
			tempOH = &bo
		} else {
			log.Panicf("Coudln't create Bundled output for s3")
		}
	case "plugin":
		log.Debugf("plugin outputmap = %s", outputMap)
		path, ok := outputMap["path"].(string)
		if !ok {
			return tempOH, errors.New("Couldn't find path in plugin output section")
		}
		name, ok := outputMap["name"].(string)
		if !ok {
			return tempOH, errors.New("Couldn't find plugin name in plugin output section")
		}

		cfg := make(map[interface{}]interface{})
		if cm, ok := outputMap["config"]; ok {
			if c, ok := cm.(map[interface{}]interface{}); ok {
				cfg = c
			} else {
				log.Errorf("failed to convert plugin config")
				switch t := cm.(type) {
				default:
					log.Errorf("Real type is %T for %s", t, c)
				}

			}
		}
		ohp, _ := loadOutputFromPlugin(path, name, cfg, myencoder)
		tempOH = ohp
	default:
		return tempOH, errors.New("No output detected...check documentation and configuration")
	}
	return tempOH, nil
}
