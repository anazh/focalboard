/*
 * @Author: your name
 * @Date: 2021-04-24 22:20:51
 * @LastEditTime: 2021-04-26 22:30:26
 * @LastEditors: Please set LastEditors
 * @Description: In User Settings Edit
 * @FilePath: \server\server\initHandlers.go
 */
package server

import (
	"log"

	"github.com/mattermost/focalboard/server/einterfaces"
)

//启动服务
func (s *Server) initHandlers() {
	cfg := s.config
	if cfg.AuthMode == "mattermost" && mattermostAuth != nil { //如果是mattermost 认证模式
		log.Println("Using Mattermost Auth")
		params := einterfaces.MattermostAuthParameters{
			ServerRoot:      cfg.ServerRoot,
			MattermostURL:   cfg.MattermostURL,
			ClientID:        cfg.MattermostClientID,
			ClientSecret:    cfg.MattermostClientSecret,
			UseSecureCookie: cfg.SecureCookie,
		}
		mmauthHandler := mattermostAuth(params, s.store)
		log.Println("CREATING AUTH")
		s.webServer.AddRoutes(mmauthHandler)
		log.Println("ADDING ROUTES")
		s.api.WorkspaceAuthenticator = mmauthHandler
		log.Println("SETTING THE AUTHENTICATOR")
	}
}
