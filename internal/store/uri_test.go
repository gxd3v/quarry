package store

import (
	"reflect"
	"testing"

	"github.com/gxd3v/quarry/internal/driver"
)

func TestParseURI(t *testing.T) {
	tests := []struct {
		uri  string
		want driver.Config
		pass string
	}{
		{
			"postgres://app:s3cret@db.example.com:5433/orders?sslmode=require&application_name=quarry",
			driver.Config{Kind: driver.Postgres, Host: "db.example.com", Port: 5433, User: "app", Database: "orders",
				Options: map[string]string{"sslmode": "require", "application_name": "quarry"}},
			"s3cret",
		},
		{
			"PostgreSQL://app@localhost/orders",
			driver.Config{Kind: driver.Postgres, Host: "localhost", User: "app", Database: "orders"},
			"",
		},
		{
			"postgresql://us%40er:p%40ss%3Aw%2Fo%25rd@[::1]:5432/my%20db",
			driver.Config{Kind: driver.Postgres, Host: "::1", Port: 5432, User: "us@er", Database: "my db"},
			"p@ss:w/o%rd",
		},
		{
			"postgres://u:raw@pass@[fe80::1]/db",
			driver.Config{Kind: driver.Postgres, Host: "fe80::1", User: "u", Database: "db"},
			"raw@pass",
		},
		{
			"postgres://u@h/db?password=fromquery&sslmode=disable",
			driver.Config{Kind: driver.Postgres, Host: "h", User: "u", Database: "db",
				Options: map[string]string{"sslmode": "disable"}},
			"fromquery",
		},
		{
			"postgres:///db",
			driver.Config{Kind: driver.Postgres, Database: "db"},
			"",
		},
		{
			"mongodb://admin:pw@mongo1:27017/app?authSource=admin&replicaSet=rs0",
			driver.Config{Kind: driver.MongoDB, Host: "mongo1", Port: 27017, User: "admin", Database: "app",
				Options: map[string]string{"authSource": "admin", "replicaSet": "rs0"}},
			"pw",
		},
		{
			"mongodb://m1:27017,m2,m3:27018/?replicaSet=rs0",
			driver.Config{Kind: driver.MongoDB, Host: "m1:27017,m2,m3:27018",
				Options: map[string]string{"replicaSet": "rs0"}},
			"",
		},
		{
			"mongodb+srv://user:pw@cluster0.abcde.mongodb.net/test?retryWrites=true&w=majority",
			driver.Config{Kind: driver.MongoDB, Host: "cluster0.abcde.mongodb.net", User: "user", Database: "test",
				Options: map[string]string{"srv": "true", "retryWrites": "true", "w": "majority"}},
			"pw",
		},
		{
			"redis://:secret@cache:6380/3",
			driver.Config{Kind: driver.Redis, Host: "cache", Port: 6380, Database: "3"},
			"secret",
		},
		{
			"rediss://default:pw@cache.example.com",
			driver.Config{Kind: driver.Redis, Host: "cache.example.com", User: "default",
				Options: map[string]string{"tls": "true"}},
			"pw",
		},
		{
			"oracle://scott:tiger@ora.example.com:1521/ORCLPDB1",
			driver.Config{Kind: driver.Oracle, Host: "ora.example.com", Port: 1521, User: "scott",
				Options: map[string]string{"service": "ORCLPDB1"}},
			"tiger",
		},
	}
	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			got, pass, err := ParseURI(tt.uri)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("config\n got %+v\nwant %+v", got, tt.want)
			}
			if pass != tt.pass {
				t.Errorf("password = %q, want %q", pass, tt.pass)
			}
		})
	}
}

func TestParseURIErrors(t *testing.T) {
	for _, uri := range []string{
		"",
		"localhost:5432",
		"mysql://u@h/db",
		"postgres://h:99999/db",
		"postgres://h:abc/db",
		"postgres://[::1/db",
		"mongodb+srv://cluster:27017/db",
		"redis://h/notanumber",
		"postgres://u:%zz@h/db",
	} {
		if _, _, err := ParseURI(uri); err == nil {
			t.Errorf("ParseURI(%q) succeeded", uri)
		}
	}
}

func TestFormatURI(t *testing.T) {
	tests := []struct {
		cfg  driver.Config
		want string
	}{
		{
			driver.Config{Kind: driver.Postgres, Host: "db", Port: 5432, User: "app", Database: "orders",
				Options: map[string]string{"sslmode": "require", "application_name": "quarry"}},
			"postgres://app@db:5432/orders?application_name=quarry&sslmode=require",
		},
		{
			driver.Config{Kind: driver.Postgres, Host: "::1", User: "us@er", Database: "my db"},
			"postgres://us%40er@[::1]/my%20db",
		},
		{
			driver.Config{Kind: driver.Postgres, Host: "::1", Port: 5432},
			"postgres://[::1]:5432",
		},
		{
			driver.Config{Kind: driver.MongoDB, Host: "cluster0.mongodb.net", User: "u",
				Options: map[string]string{"srv": "true", "authSource": "admin"}},
			"mongodb+srv://u@cluster0.mongodb.net/?authSource=admin",
		},
		{
			driver.Config{Kind: driver.MongoDB, Host: "m1:27017,m2"},
			"mongodb://m1:27017,m2",
		},
		{
			driver.Config{Kind: driver.Redis, Host: "cache", Port: 6380, Database: "3",
				Options: map[string]string{"tls": "true"}},
			"rediss://cache:6380/3",
		},
		{
			driver.Config{Kind: driver.Oracle, Host: "ora", Port: 1521, User: "scott",
				Options: map[string]string{"service": "ORCLPDB1"}},
			"oracle://scott@ora:1521/ORCLPDB1",
		},
		{driver.Config{Kind: "mysql", Host: "h"}, ""},
	}
	for _, tt := range tests {
		if got := FormatURI(tt.cfg); got != tt.want {
			t.Errorf("FormatURI(%+v) = %q, want %q", tt.cfg, got, tt.want)
		}
	}
}

func TestURIRoundTrip(t *testing.T) {
	for _, uri := range []string{
		"postgres://app@db.example.com:5433/orders?sslmode=require",
		"postgres://us%40er@[::1]:5432/my%20db",
		"mongodb+srv://u@cluster0.mongodb.net/test?authSource=admin&w=majority",
		"mongodb://m1:27017,m2,m3:27018/?replicaSet=rs0",
		"rediss://default@cache:6380/3",
		"redis://cache",
		"oracle://scott@ora:1521/ORCLPDB1",
	} {
		cfg, _, err := ParseURI(uri)
		if err != nil {
			t.Fatal(err)
		}
		if got := FormatURI(cfg); got != uri {
			t.Errorf("FormatURI(ParseURI(%q)) = %q", uri, got)
		}
	}
}
