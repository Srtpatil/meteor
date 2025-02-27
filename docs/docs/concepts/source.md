# Source

When the source field is defined, Meteor will extract data from a metadata source using the details defined in the field. `type` field should define the name of Extractor you want, you can use one from this list [here](../reference/extractors.md). `config` of a extractor can be different for different Extractor and needs you to provide details to setup a connection between meteor and your source. To determine the required configurations you can visit README of each Extractor [here](https://github.com/odpf/meteor/tree/cb12c3ecf8904cf3f4ce365ca8981ccd132f35d0/plugins/extractors/README.md).

## Writing source part of your recipe

```yaml
source:
  type: kafka
  config:
    broker: broker:9092
```

| key | Description | requirement |
| :--- | :--- | :--- |
| `type` | contains the name of extractor, will be used for registry | required |
| `config` | different extractor will require different configuration | optional, depends on extractor |

To get more information about the list of extractors we have, and how to define `type` field refer [here](../reference/extractors.md).

