Pluggable Monitoring and Automation Server (PMAAS)

This is the core set of packages that contains the bare minimum pmaas instance.
Most functionality should be implemented in plugins that implement the PMAAS API
and if necessary, SPI.

 
And idea for a syntax to configure eventing / automation:

```
masterBedroom.When()
    .Temperature().LTE().Fahrenheit(80)
    .And().Hours().Between(1, 7)
    .Then(turnOffCeilingFan)
```

### Why PMAAS?

I bought some Govee Bluetooth thermometers and wanted to start using them.  I tried Home Assistant,
but found it hard to enable the plugins. I was also surprised that something that should be
lightweight recommended it its own distribution.

Additional wants:

- I should be able to run my home on a Raspberry Pi or a VM.
- I want to use a type safe / compiled language that runs on multiple platforms.
- I want it to be extensible and customizable.  If someone doesn't like my template implementation,
  they should be able to replace it.
- I like config as code that can be put under version control.
- Plugins should be able to evolve independently of the core.  Ideally, the core can evolve without
  breaking existing plugins.

### Why Go?

I wanted to learn Go.  It's fairly simple, type safe and fast.  It has good support for cross-platform
development. I can develop on my Mac and create binaries for my Raspberry Pis.

I can check out a template assembly, add some plugins, configure it
and execute it with `go run home-assembly`.  Go downloads the dependencies automatically,
hopefully compiles and runs.

The Go dependency management and versioning support are great.  I think they compare well with the java
repo managers, but support source repositories directly.

Notes:

Working on EntityManager

A registry of entities that a plugin can contribute to the system.
Each entity has an ID, a source plugin and a Type.
The idea is that other plugins can register to receive entity add/remove events.

The convention is that the EntityManger itself can store references to the entities, since
a plugin should deregister them when they are no-longer needed or on plugin stop.  However,
references should never be exposed to other plugins to avoid memory leaks.  Rather, other
plugins can use the IDs to get stubs for making calls on the entities.

#### 2025-06-08
I updated the threading model as described in the previous entry.  The one difference from the
original plan is that all lifecycle calls, including `Init` go through the plugin's plugin runner
goroutine.  pluginWithConfig exposes internal methods that allow both synchronous & asynchronous
callback execution.   

#### 2024-05-09
I've been thinking about the threading model.  Currently, incoming function
calls on the plugin instance may come in on arbitrary goroutine.  To make it safe, I've been
implementing, or planning to implement channels that will invoke the logic on the plugin's
goroutine.

However, that's a lot of repetitive complexity on each plugin.  Instead, the
core could run the plugin lifecycle methods in each plugin's own dedicated goroutine and 
implement the channel mechanism to execute callbacks in the plugin's goroutine.

The lifecycle would look something like this:

- Plugin instantiation and init: main goroutine
- Start, Pump, Stop: plugin goroutine

I suppose init could come from the plugin goroutine as well, but we want all plugins 
to complete init before start is called.  Provider (i.e., renderer) registration must
complete before `start` so plugins can use them.

Complex plugins that want more control over their concurrency could configure the core to
skip the channel transfer and just invoke the callback directly.