<!--
 * @Author: jinde.zgm
 * @Date: 2020-12-04 23:43:30
 * @Descripttion: 
-->
## konfig
konfig is a configuration system implemented with kubernetes' ConfigMap. The service deployed through kubernetes can use configmap to achieve hot configuration updates. It can watch the modification of ConfigMap and update the configuration in time.   konfig can recursively unmarshal data in yaml format beginning with "---\n",then get the configuration through multi-level keys. For example, the following ConfigMap:
```yaml
apiVersion: v1
kind: ConfigMap
metadata:  
  namespace: test  
  name: test
data:  
  key1: |     
    ---    
    key3:       
      key5: 0.9  
  key2: |     
    ---    
    key4:      
      key6: false
```
and get multi-level configuration as the following code:
```go
import github.com/jindezgm/konfig
kfg,_ := konfig.NewWithClientset(...)
kfg.GetFloat64("key1", "key3", "key5")
kfg.GetBool("key2", "key4", "key6")
```
All configurations in konfig have revision, equal to the revision of ConfigMap. Revision equal to 0 means that the ConfigMap does not exist, and -1 means the configuration does not exist. The application can get the latest configuration from konfig when needed, and also cache the revision and get the latest configuration when there is a version update. For example:
```go
import github.com/jindezgm/konfig
...
type MyConfig struct {    
    Bool   bool   `json:"bool"`    
    Int    int    `json:"int"`    
    String string `json:"string"`
}
var my MyStruct
var rev int64
for {    
    if r := kfg.Revision(); r > rev {        
        rev = kfg.GetValue(&my, "json", "my")    
    }    
    ...
}
```
You can also register the specified type for the specified keys, then get through Get() interface without revision comparation.
```go
kfg.RegValue(&MyConfig{}, "json", "my")
for {    
    value, _ = kfg.Get("my")    
    my := value.(*MyConfig)    
    ...
}
```
It is also possible to mount the specified keys to the environment variable.
```go
kfg.MountEnv() 
defer kfg.UnmountEnv()
for {    
    if strings.ToLower(os.Getenv("print")) == "true" {        
        fmt.Println("Hello world")    
    }
}
```