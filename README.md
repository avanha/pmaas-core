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
