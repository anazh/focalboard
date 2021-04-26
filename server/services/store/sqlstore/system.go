/*
 * @Author: your name
 * @Date: 2021-04-24 22:20:51
 * @LastEditTime: 2021-04-26 19:42:55
 * @LastEditors: Please set LastEditors
 * @Description: In User Settings Edit
 * @FilePath: \server\services\store\sqlstore\system.go
 */
package sqlstore

//获取系统设置路径
func (s *SQLStore) GetSystemSettings() (map[string]string, error) {
	query := s.getQueryBuilder().Select("*").From(s.tablePrefix + "system_settings") //sql查询

	rows, err := query.Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := map[string]string{}

	for rows.Next() {
		var id string
		var value string

		err := rows.Scan(&id, &value)
		if err != nil {
			return nil, err
		}

		results[id] = value
	}

	return results, nil
}

func (s *SQLStore) SetSystemSetting(id, value string) error {
	query := s.getQueryBuilder().Insert(s.tablePrefix+"system_settings").Columns("id", "value").Values(id, value)

	_, err := query.Exec()
	if err != nil {
		return err
	}

	return nil
}
