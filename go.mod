module github.com/mycujoo/kube-deploy

go 1.12

require (
	github.com/google/btree v1.0.0 // indirect
	github.com/gregjones/httpcache v0.0.0-20190611155906-901d90724c79 // indirect
	github.com/imdario/mergo v0.3.8 // indirect
	github.com/simonleung8/flags v0.0.0-20170704170018-8020ed7bcf1a
	golang.org/x/crypto v0.0.0-20191002192127-34f69633bfdc // indirect
	golang.org/x/oauth2 v0.0.0-20190604053449-0f29369cfe45 // indirect
	golang.org/x/time v0.0.0-20190921001708-c4c64cad1fd0 // indirect
	gopkg.in/yaml.v2 v2.2.4
	k8s.io/api v0.0.0-20191009075622-910e671eb668
	k8s.io/apimachinery v0.0.0-20191006235458-f9f2f3f8ab02
	k8s.io/client-go v10.0.0+incompatible
	k8s.io/utils v0.0.0-20190923111123-69764acb6e8e // indirect
)

replace k8s.io/client-go => k8s.io/client-go v0.0.0-20190620085101-78d2af792bab
