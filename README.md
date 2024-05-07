Pluggable Monitoring and Automation Server (PMAAS)

This is the core set of packages that contains the bare minimum pmaas instance.
Most functionality should be implemented in plugins that implement the PMAAS SPI.

 
And idea for a syntax to configure eventing / automation:

masterBedroom.when()
    .temperature().lte().farenheit(80)
    .and().time().between(1, 7)
    .then(turnOffCelingFan)
