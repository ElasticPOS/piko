package upstream

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/andydunstall/piko/server/status"
)

type Status struct {
	manager *LoadBalancedManager
}

func NewStatus(manager *LoadBalancedManager) *Status {
	return &Status{
		manager: manager,
	}
}

func (s *Status) Register(group *gin.RouterGroup) {
	group.GET("/endpoints", s.listEndpointsRoute)
	group.GET("/endpoints/:endpointID", s.listEndpointUpstreamsRoute)
}

func (s *Status) listEndpointsRoute(c *gin.Context) {
	endpoints := s.manager.Endpoints()
	c.JSON(http.StatusOK, endpoints)
}

// listEndpointUpstreamsRoute lists the upstreams connected to this node for
// the requested endpoint.
//
// Endpoints may have upstreams on other nodes, which are listed by querying
// those nodes ('?forward=<node ID>').
func (s *Status) listEndpointUpstreamsRoute(c *gin.Context) {
	upstreams := s.manager.Upstreams(c.Param("endpointID"))
	c.JSON(http.StatusOK, upstreams)
}

var _ status.Handler = &Status{}
