Pluggable Monitoring and Automation Server (PMAAS)

This is the core set of packages that contains the bare minimum pmaas instance.
Most functionality should be implemented in plugins that implement the PMAAS SPI.

 
And idea for a syntax to configure eventing / automation:

masterBedroom.When()
    .Temperature().LTE().Fahrenheit(80)
    .And().Hours().Between(1, 7)
    .Then(turnOffCeilingFan)

Notes:

Working on EntityManager - A registry of entities that a plugin can contribute to the system.
Each entity has an ID, a source plugin and a Type.
The idea is that other plugins can register to receive entity add/remove events.

We probably don't want to store the entities themselves to avoid leaks.  Rather other
plugins can use the IDs to obtain information about them (or some kind of reference).

#### 2025-06-08
I updated threading model as described in the previous entry.  The one difference from the
original plan is that all lifecycle, including init go through the plugin's plugin runner
goroutine.  pluginWithConfig exposes internal methods that allow both sync & async callback 
execution and the server uses sync for the call to Init. 

#### 2024-05-09
I've been thinking about the threading model.  Currently, to be safe, incoming function
calls may come in on arbitrary goroutine.  To get around it, I've been implementing,
or planning to implement channels that will invoke the logic on the plugin's goroutine.

However, that's a lot of repetitive complexity on each plugin.  Instead, the
core could run the plugin lifecycle methods in each plugin's own goroutine, and implement the
channel mechanism to execute callbacks in the plugin's goroutine.

The lifecycle would look something like this:

- Plugin instantiation and init: main goroutine
- Start, Pump, Stop: plugin goroutine

I suppose init could come from the plugin goroutine as well, but we want all plugins 
to complete init before start is called.  Provider (i.e. renderer) registration needs to
complete before start so plugins can use them.

Complex plugins that want more control over their concurrency could configure the core to
skip the channel transfer and just invoke the callback directly.