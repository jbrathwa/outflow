package location

var regionCountries = map[string]string{
	"azure/eastus":      "US",
	"azure/eastus2":     "US",
	"azure/westus2":     "US",
	"azure/centralus":   "US",
	"azure/westeurope":  "NL",
	"azure/northeurope": "IE",
	"azure/uksouth":     "GB",
	"aws/us-east-1":     "US",
	"aws/us-west-2":     "US",
	"aws/eu-west-1":     "IE",
	"aws/eu-central-1":  "DE",
	"gcp/us-central1":   "US",
	"gcp/europe-west4":  "NL",
}

// RegionCountry returns the ISO country for a cloud region, or "" if unknown.
func RegionCountry(provider, region string) string {
	return regionCountries[provider+"/"+region]
}
