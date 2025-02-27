//go:build plugins
// +build plugins

package mysql_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"testing"

	"github.com/odpf/meteor/test/utils"

	"database/sql"

	_ "github.com/go-sql-driver/mysql"
	"github.com/odpf/meteor/models"
	commonv1beta1 "github.com/odpf/meteor/models/odpf/assets/common/v1beta1"
	facetsv1beta1 "github.com/odpf/meteor/models/odpf/assets/facets/v1beta1"
	assetsv1beta1 "github.com/odpf/meteor/models/odpf/assets/v1beta1"
	"github.com/odpf/meteor/plugins"
	"github.com/odpf/meteor/plugins/extractors/mysql"
	"github.com/odpf/meteor/test/mocks"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"github.com/stretchr/testify/assert"
)

var db *sql.DB

const (
	user = "meteor_test_user"
	pass = "pass"
	port = "3310"
)

var host = "localhost:" + port

func TestMain(m *testing.M) {
	// setup test
	opts := dockertest.RunOptions{
		Repository: "mysql",
		Tag:        "8.0.25",
		Env: []string{
			"MYSQL_ALLOW_EMPTY_PASSWORD=true",
		},
		ExposedPorts: []string{"3306", port},
		PortBindings: map[docker.Port][]docker.PortBinding{
			"3306": {
				{HostIP: "0.0.0.0", HostPort: port},
			},
		},
	}
	// exponential backoff-retry, because the application in the container might not be ready to accept connections yet
	retryFn := func(resource *dockertest.Resource) (err error) {
		db, err = sql.Open("mysql", "root@tcp("+host+")/")
		if err != nil {
			return err
		}
		return db.Ping()
	}
	purgeFn, err := utils.CreateContainer(opts, retryFn)
	if err != nil {
		log.Fatal(err)
	}
	if err := setup(); err != nil {
		log.Fatal(err)
	}

	// run tests
	code := m.Run()

	// clean tests
	db.Close()
	if err := purgeFn(); err != nil {
		log.Fatal(err)
	}
	os.Exit(code)
}

func TestInit(t *testing.T) {
	t.Run("should return error for invalid configs", func(t *testing.T) {
		err := mysql.New(utils.Logger).Init(context.TODO(), map[string]interface{}{
			"invalid_config": "invalid_config_value",
		})

		assert.Equal(t, plugins.InvalidConfigError{}, err)
	})
}

func TestExtract(t *testing.T) {
	t.Run("should extract and output tables metadata along with its columns", func(t *testing.T) {
		ctx := context.TODO()
		extr := mysql.New(utils.Logger)

		err := extr.Init(ctx, map[string]interface{}{
			"connection_url": fmt.Sprintf("%s:%s@tcp(%s)/", user, pass, host),
		})
		if err != nil {
			t.Fatal(err)
		}

		emitter := mocks.NewEmitter()
		err = extr.Extract(ctx, emitter.Push)

		assert.NoError(t, err)
		assert.Equal(t, getExpected(), emitter.Get())
	})
}

func setup() (err error) {
	testDB := "mockdata_meteor_metadata_test"

	// create database, user and grant access
	err = execute(db, []string{
		fmt.Sprintf("DROP DATABASE IF EXISTS %s", testDB),
		fmt.Sprintf("CREATE DATABASE %s", testDB),
		fmt.Sprintf("USE %s;", testDB),
		fmt.Sprintf(`CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '%s';`, user, pass),
		fmt.Sprintf(`GRANT ALL PRIVILEGES ON *.* TO '%s'@'%%';`, user),
	})
	if err != nil {
		return
	}

	// create and populate tables
	err = execute(db, []string{
		"CREATE TABLE applicant (applicant_id int, last_name varchar(255), first_name varchar(255));",
		"INSERT INTO applicant VALUES (1, 'test1', 'test11');",
		"CREATE TABLE jobs (job_id int, job varchar(255), department varchar(255));",
		"INSERT INTO jobs VALUES (2, 'test2', 'test22');",
	})
	if err != nil {
		return
	}

	return
}

func execute(db *sql.DB, queries []string) (err error) {
	for _, query := range queries {
		_, err = db.Exec(query)
		if err != nil {
			return
		}
	}
	return
}

func getExpected() []models.Record {
	return []models.Record{
		models.NewRecord(&assetsv1beta1.Table{
			Resource: &commonv1beta1.Resource{
				Urn:  "mockdata_meteor_metadata_test.applicant",
				Name: "applicant",
			},
			Schema: &facetsv1beta1.Columns{
				Columns: []*facetsv1beta1.Column{
					{
						Name:        "applicant_id",
						DataType:    "int",
						Description: "",
						IsNullable:  true,
						Length:      0,
					},
					{
						Name:        "first_name",
						DataType:    "varchar",
						Description: "",
						IsNullable:  true,
						Length:      255,
					},
					{
						Name:        "last_name",
						DataType:    "varchar",
						Description: "",
						IsNullable:  true,
						Length:      255,
					},
				},
			},
		}),
		models.NewRecord(&assetsv1beta1.Table{
			Resource: &commonv1beta1.Resource{
				Urn:  "mockdata_meteor_metadata_test.jobs",
				Name: "jobs",
			},
			Schema: &facetsv1beta1.Columns{
				Columns: []*facetsv1beta1.Column{
					{
						Name:        "department",
						DataType:    "varchar",
						Description: "",
						IsNullable:  true,
						Length:      255,
					},
					{
						Name:        "job",
						DataType:    "varchar",
						Description: "",
						IsNullable:  true,
						Length:      255,
					},
					{
						Name:        "job_id",
						DataType:    "int",
						Description: "",
						IsNullable:  true,
						Length:      0,
					},
				},
			},
		}),
	}
}
