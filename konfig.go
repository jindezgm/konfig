/*
 * @Author: jinde.zgm
 * @Date: 2020-12-04 23:01:00
 * @Descripttion:
 */

package konfig

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/jindezgm/concurrent"
	"github.com/mitchellh/mapstructure"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	clientv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/yaml"
)

// Enum error revision.
const (
	NotFound    = -1 // ConfigMap or keys not found
	ParseFailed = -2 // Parse value failed
)

// Interface define konfig interface.
type Interface interface {
	// If the specified type has been registered, return the registered type object, otherwise return the primitive type
	Get(keys ...string) (interface{}, int64)
	// Get value in boolean type, convert if it is not boolean:
	// number: !=0 is true, ==0 is false
	// string: Ignore case TrUe is true, ignore case FaLsE is true,
	GetBool(keys ...string) (bool, int64)
	// Get value in int64 type, convert if it is not integer:
	// number: int64(number)
	// string: string->float64->int64
	// boolean: true is 1, false is 0
	GetInt64(keys ...string) (int64, int64)
	// Get value in int type.
	GetInt(keys ...string) (int, int64)
	// Get value in int32 type.
	GetInt32(keys ...string) (int32, int64)
	// Get value in float64 type, convert if it is not number:
	// string: string->float64
	GetFloat64(keys ...string) (float64, int64)
	// Get value in float32 type.
	GetFloat32(keys ...string) (float32, int64)
	// Get value in string type, convert if it is not string:
	// any: fmt.Sprintf("%v", any)
	GetString(keys ...string) (string, int64)
	// Register the values under specific keys as a type of object, and then get the object by Get().
	// type LogConfig struct {
	//     LogLevel     int  `json:"logLevel"`
	//     AlsoToStderr bool `json:"alsoToStderr"`
	// }
	// RegValue(&LogConfigs{}, "json", "log")
	// for {
	//     logConfig, rev := Get("log")
	//     if logConfig.AlsoToStderr {
	//         fmt.Println(...)
	//     }
	// }
	RegValue(ptr interface{}, tag string, keys ...string) (interface{}, int64)
	// Parse the value under specific keys into an object of the specified type.
	GetValue(ptr interface{}, tag string, keys ...string) int64
	// Mount the values under specific keys to environment variables
	MountEnv(keys ...string)
	UnmountEnv()
	// Get revision of ConfigMap.
	Revision() int64
}

// NewWithClientset create konfig with kubernetes client set.
func NewWithClientset(ctx context.Context, clientset *kubernetes.Clientset, name, namespace string) (Interface, error) {
	c := &configmap{
		client: clientset.CoreV1().ConfigMaps(namespace),
		ctx:    ctx,
		name:   name,
	}

	// Initialize state.
	if err := c.load(); nil != err {
		return nil, err
	}

	// Watch ConfigMap.
	watch, err := c.client.Watch(c.ctx, metav1.ListOptions{})
	if nil != err {
		return nil, err
	}

	go c.runWithClientset(watch)
	return c, nil
}

// NewWithSharedInformerFactory create konfig with shared informer factory.
func NewWithSharedInformerFactory(ctx context.Context, factory informers.SharedInformerFactory, name, namespace string) Interface {
	c := &configmap{
		ctx:       ctx,
		name:      name,
		namespace: namespace,
		informer:  factory.Core().V1().ConfigMaps().Informer(),
		cmc:       make(chan *corev1.ConfigMap, 10),
	}

	// Add ConfigMap event handler.
	c.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.add,
		UpdateFunc: c.update,
		DeleteFunc: c.delete,
	})

	go c.runWithInformer()
	return c
}

// configmap implement Interface.
type configmap struct {
	client    clientv1.ConfigMapInterface // Used for clientset mode.
	informer  cache.SharedIndexInformer   // Used for informer mode.
	ctx       context.Context             // Context.
	name      string                      // ConfigMap name.
	namespace string                      // ConfigMap namespace.
	rev       string                      // ConfigMap recent revision.
	cmc       chan *corev1.ConfigMap      // Used for informer mode to buffer ConfigMap.
	values    atomic.Value                // Semi-finished products(revisionedValues) of recent ConfigMap.
	registery concurrent.NestedMap        // Registered types
	envKeys   atomic.Value                // The keys of the environment variables are mounted
}

// revisionedValues is another format of ConfigMap, it did a deeper deserialization of the data.
type revisionedValues struct {
	revision int64                  // ConfigMap revision.
	values   map[string]interface{} // Semi-finished products(map[string]interface{})
}

// registeredValue define registered value.
type registeredValue struct {
	typ   reflect.Type // Value type.
	rev   int64        // ConfigMap revision.
	keys  []string     // Keys of value.
	tag   string       // Tag name of value type member variable
	value atomic.Value // Value pointer
}

// parseValue parse registered value.
func (rv *registeredValue) parseValue(c *configmap) int64 {
	value := reflect.New(rv.typ).Interface()
	if rv.rev = c.GetValue(value, rv.tag, rv.keys...); rv.rev != ParseFailed {
		// NotFound will also be written, just the initial value
		rv.value.Store(value)
	}

	return rv.rev
}

// Define root keys for concurrent.NestedMap.
type rootKeys struct{}

// Get implements Interface.Get().
func (c *configmap) Get(keys ...string) (interface{}, int64) {
	// Check the registration form first
	if value, rev, exist := c.getFromRegistery(keys...); exist {
		return value, rev
	}

	return c.get(keys...)
}

// GetBool implements Interface.GetBool().
func (c *configmap) GetBool(keys ...string) (bool, int64) {
	if value, rev := c.get(keys...); rev >= 0 {
		switch v := value.(type) {
		case bool:
			return v, rev
		case float64:
			return v != 0, rev
		case string:
			if lv := strings.ToLower(v); lv == "true" {
				return true, rev
			} else if lv == "false" {
				return false, rev
			} else if f64, err := strconv.ParseFloat(v, 64); err == nil {
				return f64 != 0, rev
			}
		}
	}

	return false, NotFound
}

// GetInt64 implements Interface.GetInt64().
func (c *configmap) GetInt64(keys ...string) (int64, int64) {
	if value, rev := c.get(keys...); rev >= 0 {
		switch v := value.(type) {
		case float64:
			return int64(v), rev
		case string:
			if lv := strings.ToLower(v); lv == "true" {
				return 1, rev
			} else if lv == "false" {
				return 0, rev
			} else if v64, err := strconv.ParseFloat(v, 64); err == nil {
				return int64(v64), rev
			}
		case bool:
			if v {
				return 1, rev
			}
			return 0, rev
		}
	}

	return 0, NotFound
}

// GetInt implements Interface.GetInt().
func (c *configmap) GetInt(keys ...string) (int, int64) {
	if i64, rev := c.GetInt64(keys...); rev >= 0 {
		return int(i64), rev
	}

	return 0, NotFound
}

// GetInt32 implements Interface.GetInt32().
func (c *configmap) GetInt32(keys ...string) (int32, int64) {
	if i64, rev := c.GetInt64(keys...); rev >= 0 {
		return int32(i64), rev
	}

	return 0, NotFound
}

// GetFloat64 implements Interface.GetFloat64().
func (c *configmap) GetFloat64(keys ...string) (float64, int64) {
	if value, rev := c.get(keys...); rev >= 0 {
		switch v := value.(type) {
		case float64:
			return v, rev
		case string:
			if v64, err := strconv.ParseFloat(v, 64); err == nil {
				return v64, rev
			}
		}
	}

	return 0, NotFound
}

// GetFloat32 implements Interface.GetFloat32().
func (c *configmap) GetFloat32(keys ...string) (float32, int64) {
	if f64, rev := c.GetFloat64(keys...); rev >= 0 {
		return float32(f64), rev
	}

	return 0, NotFound
}

// GetString implements Interface.GetString().
func (c *configmap) GetString(keys ...string) (string, int64) {
	if value, rev := c.get(keys...); rev >= 0 {
		if sv, ok := value.(string); ok {
			return sv, rev
		}
		return fmt.Sprintf("%v", value), rev
	}

	return "", NotFound
}

// RegValue implement Interface.RegValue().
func (c *configmap) RegValue(ptr interface{}, tag string, keys ...string) (interface{}, int64) {
	// Must be pointer type
	typ := reflect.TypeOf(ptr)
	if typ.Kind() != reflect.Ptr {
		return nil, ParseFailed
	}

	// Create registered value and parse it's value.
	rv := &registeredValue{typ: typ.Elem(), tag: tag, keys: append([]string{}, keys...)}
	if rev := rv.parseValue(c); rev != ParseFailed {
		if len(keys) == 0 {
			c.registery.Store(rv, rootKeys{})
		} else {
			c.registery.Store(rv, keys)
		}
	}

	return rv.value.Load(), rv.rev
}

// MountEnv implements Interface.MountEnv().
func (c *configmap) MountEnv(keys ...string) {
	c.envKeys.Store(append([]string{}, keys...))
	c.mountEnv()
}

// UnmountEnv implements Interface.UnmountEnv().
func (c *configmap) UnmountEnv() {
	c.envKeys = atomic.Value{}
}

// GetValue implement Interface.GetValue().
func (c *configmap) GetValue(ptr interface{}, tag string, keys ...string) int64 {
	if value, rev := c.get(keys...); rev >= 0 {
		if m, ok := value.(map[string]interface{}); ok {
			cfg := mapstructure.DecoderConfig{
				Metadata:         nil,
				Result:           ptr,
				TagName:          tag,
				WeaklyTypedInput: true,
			}

			if decoder, err := mapstructure.NewDecoder(&cfg); nil == err {
				if err = decoder.Decode(m); err == nil {
					return rev
				}
			}
		}
		return ParseFailed
	}
	return NotFound
}

// Revision implements Interface.Revision().
func (c *configmap) Revision() int64 {
	return c.values.Load().(*revisionedValues).revision
}

// load initialize ConfigMap state.
func (c *configmap) load() error {
	// Get ConfigMap.
	cm, err := c.client.Get(c.ctx, c.name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		c.values.Store(new(revisionedValues))
		return nil
	} else if nil != err {
		return err
	}

	// Initialize state.
	c.rev = cm.ResourceVersion
	c.parse(cm.ResourceVersion, cm.Data)

	return nil
}

// getFromRegistery get the value from registery.
func (c *configmap) getFromRegistery(keys ...string) (interface{}, int64, bool) {
	// Check the registration form first
	var value interface{}
	var err error
	if len(keys) == 0 {
		value, err = c.registery.Load(rootKeys{})
	} else {
		value, err = c.registery.Load(keys)
	}

	// Return the registered type value.
	if nil == err {
		if rv, ok := value.(*registeredValue); ok {
			return rv.value.Load(), rv.rev, true
		}
	}

	return nil, NotFound, false
}

// get the value of the primitive type
func (c *configmap) get(keys ...string) (interface{}, int64) {
	if rvs, ok := c.values.Load().(*revisionedValues); ok && len(rvs.values) != 0 {
		// Return root if not specific any keys.
		m := rvs.values
		if len(keys) == 0 {
			return m, rvs.revision
		}

		// Search keys and return value.
		for i, key := range keys {
			if value, ok := m[key]; !ok {
				break
			} else if i == len(keys)-1 {
				return value, rvs.revision
			} else if m, ok = value.(map[string]interface{}); !ok {
				break
			}
		}
	}

	return nil, NotFound
}

// parse covert ConfigMap to revisionedValues.
func (c *configmap) parse(revision string, data map[string]string) {
	// Convert revison from string to int64
	rev, err := strconv.ParseInt(revision, 10, 64)
	if nil != err {
		panic(err)
	}

	// Convert ConfigMap data.
	rvs := &revisionedValues{values: make(map[string]interface{}, len(data)), revision: rev}
	for n, v := range data {
		// If it starts with "---", it will continue to use yaml to deserialize
		if len(v) > 4 && v[:4] == "---\n" {
			var b map[string]interface{}
			if err := yaml.Unmarshal([]byte(v[4:]), &b); err == nil {
				rvs.values[n] = b
				continue
			}
		}
		// Copy data.
		rvs.values[n] = v
	}

	c.values.Store(rvs)

	// Update registered values.
	c.registery.Range(func(keys []interface{}, value interface{}) bool {
		if rv, ok := value.(*registeredValue); !ok {
			panic(value)
		} else {
			rv.parseValue(c)
		}
		return true
	})

	// Mount env.
	if c.envKeys.Load() != nil {
		c.mountEnv()
	}
}

// mountEnv update enviroment variables values.
func (c *configmap) mountEnv() {
	if keys, ok := c.envKeys.Load().([]string); ok {
		if value, rev := c.get(keys...); rev >= 0 {
			if m, ok := value.(map[string]interface{}); ok {
				for k, v := range m {
					os.Setenv(k, fmt.Sprintf("%v", v))
				}
			}
		}
	}
}

// runWithClientset use clientset to watch the changes of ConfigMap and handle it in time.
func (c *configmap) runWithClientset(w watch.Interface) {
	defer w.Stop()
	for result := range w.ResultChan() {
		if cm, ok := result.Object.(*corev1.ConfigMap); ok && cm.Name == c.name && cm.ResourceVersion > c.rev {
			switch result.Type {
			case watch.Added, watch.Modified:
				c.parse(cm.ResourceVersion, cm.Data)
			case watch.Deleted:
				c.values.Store(new(revisionedValues))
			}
			c.rev = cm.ResourceVersion
		}
	}
}

// runWithInformer handle ConfigMap changes send by informer.
func (c *configmap) runWithInformer() {
	for {
		select {
		case cm := <-c.cmc:
			c.parse(cm.ResourceVersion, cm.Data)
		case <-c.ctx.Done():
			return
		}
	}
}

// update handle ConfigMap add event.
func (c *configmap) add(obj interface{}) {
	if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == c.name && cm.Namespace == c.namespace {
		c.cmc <- cm
	}
}

// update handle ConfigMap delete event.
func (c *configmap) delete(obj interface{}) {
	if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == c.name && cm.Namespace == c.namespace {
		c.values.Store(new(revisionedValues))
	}
}

// update handle ConfigMap update event.
func (c *configmap) update(old, cur interface{}) {
	c.add(cur)
}
