// Where public-cloud regions are. A cluster's region label ("eu-central-1", "europe-west3", "westeurope") is
// a code the provider defines, so a table is a far better source than guessing from free text. Positions are
// the metro area the provider names for the region, not the street address of a data centre; they are good for
// "which country and roughly where", not for distance calculations between racks.
//
// Compiled from the providers' public region lists. New regions appear all the time: an unknown code simply
// gets no suggestion, it never gets a wrong one. Add a line to teach the app a new region.

export type CloudProvider = 'aws' | 'gcp' | 'azure' | 'hetzner' | 'ovh' | 'digitalocean' | 'scaleway'

export interface CloudRegion {
  provider: CloudProvider
  code: string
  city: string
  country: string // ISO 3166-1 alpha-2
  lat: number
  lng: number
}

type Row = [code: string, city: string, country: string, lat: number, lng: number]

const table = (provider: CloudProvider, rows: Row[]): CloudRegion[] =>
  rows.map(([code, city, country, lat, lng]) => ({ provider, code, city, country, lat, lng }))

export const CLOUD_REGIONS: CloudRegion[] = [
  ...table('aws', [
    ['us-east-1', 'Ashburn', 'US', 39.04, -77.49], ['us-east-2', 'Columbus', 'US', 39.96, -82.99],
    ['us-west-1', 'San Jose', 'US', 37.34, -121.89], ['us-west-2', 'Portland', 'US', 45.52, -122.68],
    ['ca-central-1', 'Montréal', 'CA', 45.5, -73.57], ['ca-west-1', 'Calgary', 'CA', 51.05, -114.07],
    ['mx-central-1', 'Querétaro', 'MX', 20.59, -100.39], ['sa-east-1', 'São Paulo', 'BR', -23.55, -46.63],
    ['eu-west-1', 'Dublin', 'IE', 53.35, -6.26], ['eu-west-2', 'London', 'GB', 51.51, -0.13], ['eu-west-3', 'Paris', 'FR', 48.86, 2.35],
    ['eu-central-1', 'Frankfurt', 'DE', 50.11, 8.68], ['eu-central-2', 'Zurich', 'CH', 47.38, 8.54], ['eu-north-1', 'Stockholm', 'SE', 59.33, 18.07],
    ['eu-south-1', 'Milan', 'IT', 45.46, 9.19], ['eu-south-2', 'Zaragoza', 'ES', 41.65, -0.89],
    ['me-south-1', 'Manama', 'BH', 26.23, 50.59], ['me-central-1', 'Dubai', 'AE', 25.2, 55.27], ['il-central-1', 'Tel Aviv', 'IL', 32.09, 34.78],
    ['af-south-1', 'Cape Town', 'ZA', -33.92, 18.42],
    ['ap-east-1', 'Hong Kong', 'HK', 22.32, 114.17], ['ap-south-1', 'Mumbai', 'IN', 19.08, 72.88], ['ap-south-2', 'Hyderabad', 'IN', 17.39, 78.49],
    ['ap-southeast-1', 'Singapore', 'SG', 1.35, 103.82], ['ap-southeast-2', 'Sydney', 'AU', -33.87, 151.21], ['ap-southeast-3', 'Jakarta', 'ID', -6.21, 106.85],
    ['ap-southeast-4', 'Melbourne', 'AU', -37.81, 144.96], ['ap-southeast-5', 'Kuala Lumpur', 'MY', 3.14, 101.69], ['ap-southeast-7', 'Bangkok', 'TH', 13.76, 100.5],
    ['ap-northeast-1', 'Tokyo', 'JP', 35.68, 139.69], ['ap-northeast-2', 'Seoul', 'KR', 37.57, 126.98], ['ap-northeast-3', 'Osaka', 'JP', 34.69, 135.5],
    ['cn-north-1', 'Beijing', 'CN', 39.9, 116.41], ['cn-northwest-1', 'Yinchuan', 'CN', 38.49, 106.23],
  ]),
  ...table('gcp', [
    ['us-central1', 'Council Bluffs', 'US', 41.26, -95.86], ['us-east1', 'Moncks Corner', 'US', 33.2, -80.01], ['us-east4', 'Ashburn', 'US', 39.04, -77.49],
    ['us-east5', 'Columbus', 'US', 39.96, -82.99], ['us-south1', 'Dallas', 'US', 32.78, -96.8], ['us-west1', 'The Dalles', 'US', 45.59, -121.18],
    ['us-west2', 'Los Angeles', 'US', 34.05, -118.24], ['us-west3', 'Salt Lake City', 'US', 40.76, -111.89], ['us-west4', 'Las Vegas', 'US', 36.17, -115.14],
    ['northamerica-northeast1', 'Montréal', 'CA', 45.5, -73.57], ['northamerica-northeast2', 'Toronto', 'CA', 43.65, -79.38], ['northamerica-south1', 'Querétaro', 'MX', 20.59, -100.39],
    ['southamerica-east1', 'São Paulo', 'BR', -23.55, -46.63], ['southamerica-west1', 'Santiago', 'CL', -33.45, -70.67],
    ['europe-west1', 'St. Ghislain', 'BE', 50.45, 3.82], ['europe-west2', 'London', 'GB', 51.51, -0.13], ['europe-west3', 'Frankfurt', 'DE', 50.11, 8.68],
    ['europe-west4', 'Eemshaven', 'NL', 53.44, 6.83], ['europe-west6', 'Zurich', 'CH', 47.38, 8.54], ['europe-west8', 'Milan', 'IT', 45.46, 9.19],
    ['europe-west9', 'Paris', 'FR', 48.86, 2.35], ['europe-west10', 'Berlin', 'DE', 52.52, 13.4], ['europe-west12', 'Turin', 'IT', 45.07, 7.69],
    ['europe-north1', 'Hamina', 'FI', 60.57, 27.2], ['europe-north2', 'Stockholm', 'SE', 59.33, 18.07], ['europe-central2', 'Warsaw', 'PL', 52.23, 21.01],
    ['europe-southwest1', 'Madrid', 'ES', 40.42, -3.7],
    ['me-west1', 'Tel Aviv', 'IL', 32.09, 34.78], ['me-central1', 'Doha', 'QA', 25.29, 51.53], ['me-central2', 'Dammam', 'SA', 26.43, 50.1],
    ['asia-east1', 'Changhua', 'TW', 24.07, 120.54], ['asia-east2', 'Hong Kong', 'HK', 22.32, 114.17], ['asia-northeast1', 'Tokyo', 'JP', 35.68, 139.69],
    ['asia-northeast2', 'Osaka', 'JP', 34.69, 135.5], ['asia-northeast3', 'Seoul', 'KR', 37.57, 126.98], ['asia-south1', 'Mumbai', 'IN', 19.08, 72.88],
    ['asia-south2', 'Delhi', 'IN', 28.61, 77.21], ['asia-southeast1', 'Singapore', 'SG', 1.35, 103.82], ['asia-southeast2', 'Jakarta', 'ID', -6.21, 106.85],
    ['australia-southeast1', 'Sydney', 'AU', -33.87, 151.21], ['australia-southeast2', 'Melbourne', 'AU', -37.81, 144.96], ['africa-south1', 'Johannesburg', 'ZA', -26.2, 28.05],
  ]),
  ...table('azure', [
    ['eastus', 'Ashburn', 'US', 39.04, -77.49], ['eastus2', 'Boydton', 'US', 36.67, -78.39], ['westus', 'San Francisco', 'US', 37.77, -122.42],
    ['westus2', 'Quincy', 'US', 47.23, -119.85], ['westus3', 'Phoenix', 'US', 33.45, -112.07], ['centralus', 'Des Moines', 'US', 41.59, -93.62],
    ['northcentralus', 'Chicago', 'US', 41.88, -87.63], ['southcentralus', 'San Antonio', 'US', 29.42, -98.49],
    ['canadacentral', 'Toronto', 'CA', 43.65, -79.38], ['canadaeast', 'Québec City', 'CA', 46.81, -71.21], ['brazilsouth', 'São Paulo', 'BR', -23.55, -46.63],
    ['northeurope', 'Dublin', 'IE', 53.35, -6.26], ['westeurope', 'Amsterdam', 'NL', 52.37, 4.9], ['uksouth', 'London', 'GB', 51.51, -0.13],
    ['ukwest', 'Cardiff', 'GB', 51.48, -3.18], ['francecentral', 'Paris', 'FR', 48.86, 2.35], ['francesouth', 'Marseille', 'FR', 43.3, 5.37],
    ['germanywestcentral', 'Frankfurt', 'DE', 50.11, 8.68], ['germanynorth', 'Berlin', 'DE', 52.52, 13.4], ['switzerlandnorth', 'Zurich', 'CH', 47.38, 8.54],
    ['switzerlandwest', 'Geneva', 'CH', 46.2, 6.14], ['norwayeast', 'Oslo', 'NO', 59.91, 10.75], ['swedencentral', 'Gävle', 'SE', 60.67, 17.14],
    ['polandcentral', 'Warsaw', 'PL', 52.23, 21.01], ['italynorth', 'Milan', 'IT', 45.46, 9.19], ['spaincentral', 'Madrid', 'ES', 40.42, -3.7],
    ['uaenorth', 'Dubai', 'AE', 25.2, 55.27], ['qatarcentral', 'Doha', 'QA', 25.29, 51.53], ['israelcentral', 'Tel Aviv', 'IL', 32.09, 34.78],
    ['southafricanorth', 'Johannesburg', 'ZA', -26.2, 28.05],
    ['australiaeast', 'Sydney', 'AU', -33.87, 151.21], ['australiasoutheast', 'Melbourne', 'AU', -37.81, 144.96], ['southeastasia', 'Singapore', 'SG', 1.35, 103.82],
    ['eastasia', 'Hong Kong', 'HK', 22.32, 114.17], ['japaneast', 'Tokyo', 'JP', 35.68, 139.69], ['japanwest', 'Osaka', 'JP', 34.69, 135.5],
    ['koreacentral', 'Seoul', 'KR', 37.57, 126.98], ['centralindia', 'Pune', 'IN', 18.52, 73.86], ['southindia', 'Chennai', 'IN', 13.08, 80.27], ['westindia', 'Mumbai', 'IN', 19.08, 72.88],
  ]),
  ...table('hetzner', [
    ['fsn1', 'Falkenstein', 'DE', 50.48, 12.37], ['nbg1', 'Nuremberg', 'DE', 49.45, 11.08], ['hel1', 'Helsinki', 'FI', 60.17, 24.94],
    ['ash', 'Ashburn', 'US', 39.04, -77.49], ['hil', 'Hillsboro', 'US', 45.52, -122.99], ['sin', 'Singapore', 'SG', 1.35, 103.82],
  ]),
  ...table('ovh', [
    ['gra', 'Gravelines', 'FR', 50.99, 2.13], ['sbg', 'Strasbourg', 'FR', 48.57, 7.75], ['rbx', 'Roubaix', 'FR', 50.69, 3.17], ['bhs', 'Beauharnois', 'CA', 45.32, -73.87],
    ['waw', 'Warsaw', 'PL', 52.23, 21.01], ['lon', 'London', 'GB', 51.51, -0.13], ['sgp', 'Singapore', 'SG', 1.35, 103.82], ['syd', 'Sydney', 'AU', -33.87, 151.21],
  ]),
  ...table('digitalocean', [
    ['nyc1', 'New York', 'US', 40.71, -74.01], ['nyc3', 'New York', 'US', 40.71, -74.01], ['sfo3', 'San Francisco', 'US', 37.77, -122.42],
    ['ams3', 'Amsterdam', 'NL', 52.37, 4.9], ['fra1', 'Frankfurt', 'DE', 50.11, 8.68], ['lon1', 'London', 'GB', 51.51, -0.13], ['sgp1', 'Singapore', 'SG', 1.35, 103.82],
    ['blr1', 'Bangalore', 'IN', 12.97, 77.59], ['tor1', 'Toronto', 'CA', 43.65, -79.38], ['syd1', 'Sydney', 'AU', -33.87, 151.21],
  ]),
  ...table('scaleway', [
    ['fr-par', 'Paris', 'FR', 48.86, 2.35], ['nl-ams', 'Amsterdam', 'NL', 52.37, 4.9], ['pl-waw', 'Warsaw', 'PL', 52.23, 21.01],
  ]),
]

const BY_CODE = new Map<string, CloudRegion[]>()
for (const r of CLOUD_REGIONS) BY_CODE.set(r.code, [...(BY_CODE.get(r.code) ?? []), r])

/**
 * Find the region a label refers to. Availability-zone labels ("eu-central-1a", "europe-west3-a",
 * "westeurope-1") are reduced to their region. When `provider` is known it must agree with the match.
 */
export function findCloudRegion(label: string | undefined, provider?: CloudProvider): CloudRegion | undefined {
  const raw = (label ?? '').trim().toLowerCase()
  if (!raw) return undefined
  const candidates = [raw, raw.replace(/^(.*\d)[a-z]$/, '$1'), raw.replace(/-[a-z0-9]$/, '')]
  for (const c of candidates) {
    const hits = BY_CODE.get(c)
    if (!hits) continue
    const fit = provider ? hits.find((h) => h.provider === provider) : hits[0]
    if (fit) return fit
  }
  return undefined
}
